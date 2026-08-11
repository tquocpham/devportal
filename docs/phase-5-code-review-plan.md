# Phase 5: Branch-aware code review

## Context

Deferred behind Phase 3 (AWS self-service) since AWS access is the more urgent need — people are waiting on that one — and behind Phase 4 (MCP server), which was moved ahead of this since it's a prerequisite for Phase 6's cost-cap graduation path. This phase adds the ability to point the assistant at a specific branch and get a review grounded in both the diff and the indexed main-branch codebase. It reuses the existing `search_codebase` tool machinery and Anthropic client wiring almost entirely — the only new capability is fetching a GitHub diff and handing it to Claude as extra context alongside the search tool.

Diff-only, no re-indexing (confirmed earlier — cheaper, faster, and matches how a human reviewer actually works: read the diff, check it against the codebase they already know, rather than re-learning the whole branch from scratch).

Same auth story as everywhere else in this app: `c.Get("user").(jwt.MapClaims)["sub"]` off the existing session JWT, registered in the existing `protected` route group — no new authorization layer, since anyone who can log in already passed the org/allowlist gate in `auth.go`'s `Callback`.

## Approach

**Extract the shared tool-building code first.** The `search_codebase` `anthropic.BetaTool` construction currently lives inline inside `ChatHandler.Chat` (`cmd/api/lib/handlers/chat.go`), closing over a per-request `citations`/`seen` accumulator. Pull it into a new `cmd/api/lib/handlers/searchtool.go` as `newSearchCodebaseTool(store, embedder, topK, citations *[]citation, seen map[string]bool) (anthropic.BetaTool, error)` — same `citation`/`searchInput` types move with it. `chat.go` calls the shared constructor instead of duplicating the ~35-line closure; no behavior change to `/api/v1/chat`. `ReviewHandler` (below) uses the same constructor.

**New `cmd/api/lib/handlers/review.go`** — `ReviewHandler` mirrors `ChatHandler`'s shape: `store`, `embedder`, `anthropic`, plus a `githubToken` and a `ReviewConfig` (`Model`, `MaxTokens` — bigger default than chat, e.g. 4096, reviews run longer — `TopK`, `MaxIterations`, `MaxDiffBytes`, `DefaultRepo`, `DefaultBase`), all config-driven the same way as `ChatConfig`/`DefaultChatConfig()`.

- Request: `{branch, base?, repo?}` — `base`/`repo` default from config if omitted.
- **GitHub auth for diff fetching**: the OAuth access token from login is never persisted (confirmed — it lives only in-memory during `auth.go`'s `Callback`), so it can't be reused later. Re-plumbing per-user token storage would be a bigger, riskier change than this feature needs. Instead: one new shared config key, `github_review_token` — a company-held GitHub fine-grained PAT, read-only `Contents` scope on the one repo — exactly the same pattern as `anthropic_api_key`. Powers review for every logged-in user.
- **Diff fetch**: raw `net/http` GET to `https://api.github.com/repos/{repo}/compare/{base}...{branch}` (`Accept: application/vnd.github+json`, bearer `github_review_token`), same unauthenticated-client style already used in `auth.go` for `/user`/`/user/emails`/`/user/orgs` — no new GitHub client dependency. Response gives per-file `{filename, status, additions, deletions, patch}` plus a top-level `truncated` flag.
- **Cost safeguard** (mirrors `MaxEmbeddingTokens` in `cmd/indexer/config.go:11-34`): `MaxDiffBytes` on `ReviewConfig` (default 200_000, override via `review_max_diff_bytes`). Sum patch bytes as you build the prompt; once the running total would exceed the cap, stop including further patch bodies (still list the remaining files' name/status/added/removed counts with an "[omitted — over size cap]" marker) and set `diffTruncated: true` in the response. Same handling if GitHub's own `truncated: true` fires for very large branch diffs. New `reviewSystemPrompt` const tells Claude to say so explicitly when working from a truncated diff, rather than reviewing a partial diff as if complete — same "don't guess" instinct as `chatSystemPrompt`.
- **Prompt shape**: reviewer persona (review this Unreal project's diff for correctness/safety/consistency; use `search_codebase` — still pointed at the indexed **main** branch — to check changed code against existing callers/patterns/config rather than judging the diff in isolation). Diff goes in the user turn (changes every request), persona stays in the cached system block — same split chat already uses for retrieved chunks vs. static prompt.
- Response: `{answer, files: [{filename, status, additions, deletions}], citations, diffTruncated}`.
- Route: `protected.POST("/api/v1/review", review.Review)`.

## UI (`cmd/api/web/index.html`)

Adds a third tab (`Chat | AWS Access | Review`, AWS Access having shipped in Phase 3) toggling a view `<div>` the same way the others already do.

- **Review tab**: `branch`/`base` text inputs (base defaults to `"main"`) → `POST /api/v1/review`, render `answer` as an assistant bubble same as chat, a small file list (`files[]`), citation tags reusing the existing `.citation` style, and a visible warning banner when `diffTruncated`.

## Files

- `cmd/api/lib/handlers/searchtool.go` (new) — extracted `newSearchCodebaseTool`, `citation`, `searchInput`
- `cmd/api/lib/handlers/chat.go` — refactored to use the shared constructor, no behavior change
- `cmd/api/lib/handlers/review.go` (new) — `ReviewHandler`, `ReviewConfig`, diff fetch + truncation, `Review` handler
- `cmd/api/main.go` — new review config reads, construct `ReviewHandler`, register `protected.POST("/api/v1/review", ...)`
- `cmd/api/config.local.example.yaml` — document the new `github_review_token`/`review_*` keys
- `cmd/api/web/index.html` — Review tab

## Verification

- `go build ./...` across the workspace.
- Unit tests for the diff-truncation/budget logic — pure, credential-free, and the piece with the most bug risk.
- Smoke test (needs a real `github_review_token` + a branch with an actual diff): push a throwaway branch, `POST /api/v1/review`, confirm the file list and `search_codebase` citations both show up; temporarily lower `review_max_diff_bytes` to confirm truncation triggers and is explained in the answer.
