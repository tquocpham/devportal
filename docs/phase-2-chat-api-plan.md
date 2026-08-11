# Phase 2: Retrieval-augmented `/api/v1/chat` + minimal web UI

## Context

Phase 1 (the indexer) is done and verified — 495 real, embedded chunks sitting in Postgres from a live run. Nothing can query that index yet. This phase closes the loop from the architecture doc: a designer logs into a web page (GitHub OAuth, already built), asks a question, and gets an answer grounded in the indexed code, paid for centrally via one company Anthropic key — cost-conscious per the earlier "not rich" constraint (Claude Sonnet 5, not Opus; capped tokens; cached system prompt; small top-K retrieval).

**One structural problem to solve first:** `cmd/indexer`'s `Store`, `Embedder`, and `Chunk` currently live in `package main` — Go can't import another module's `package main`, so `cmd/api` can't reuse them as-is. This finally does the `pkg/retrieval` extraction the original architecture doc called for: pull the DB/embedding code into a shared, importable module, and have both `cmd/indexer` (writes) and `cmd/api` (reads, at chat time) depend on it.

**One correctness fix while touching this code:** Voyage's docs recommend `input_type: "document"` when embedding content for storage and `input_type: "query"` when embedding a search query — asymmetric embeddings, better retrieval quality. The current `Embedder.Embed` always hardcodes `"document"`. Adding `EmbedQuery` (falls back to `Embed` for OpenAI, which has no such distinction) fixes this now that query-time embedding is about to exist for the first time.

**One deployment decision:** the pre-existing top-level `web/` directory is empty and, being outside `cmd/api`'s module boundary, can't be `//go:embed`'d into the `cmd/api` binary (Go embed paths can't cross the module root). Putting the chat page at `cmd/api/web/index.html` and embedding it gives a single self-contained deployable binary — no separate static-file directory to ship correctly alongside it, no cwd-relative path fragility. The empty top-level `web/` stays unused; not deleting it, just not building there.

## Approach

**1. Extract `pkg/retrieval`** (new Go module, added to `go.work`):
- `chunk.go` — `Chunk` struct (moved from `cmd/indexer/chunker.go`)
- `store.go` — `Store` (moved from `cmd/indexer/store.go`, unchanged)
- `embedder.go` — `Embedder` interface + `voyageEmbedder`/`openAIEmbedder` (moved from `cmd/indexer/embedder.go`, plus the new `EmbedQuery` method and existing `ErrRateLimited`/token-usage-return behavior)

`cmd/indexer` updates its imports to `pkg/retrieval` instead of local types; `chunker.go`, `walker.go`, `config.go`, `main.go` (indexing-specific) stay in `cmd/indexer`. `store.go` and `embedder.go` are deleted from `cmd/indexer` (superseded).

**2. `cmd/api` gets query-time retrieval + generation.**
- New config (via the existing viper/`config.local.yaml` pattern, same as `github_client_id` etc.): `database_url`, `embedding_provider` (+ matching key), `anthropic_api_key`. These must point at the **same** Postgres/embedding-provider the indexer used — the query embedding has to come from the same model/dimension as what's stored.
- `main.go` constructs a `retrieval.Store`, a `retrieval.Embedder`, and an `anthropic.Client` (official `github.com/anthropics/anthropic-sdk-go`, company-paid key) at startup, passes them into a new chat handler.
- New `lib/handlers/chat.go`: `POST /api/v1/chat`, added to the existing `protected` route group (reuses `RequireAuth` — no new auth code needed). Request: `{message, history}` (history capped server-side to the last ~6 turns regardless of what the client sends, as a defensive floor). Flow: `embedder.EmbedQuery(message)` → `store.Search(vector, topK=6)` → call Claude with a **cached** system prompt (persona: coding assistant for game designers on this project — plain language, always cite `file:line`, say "not found in the indexed code" rather than guess) + the retrieved chunks folded into the latest user turn + trimmed history. Model **`claude-sonnet-5`**, `max_tokens` capped (2048), `effort: medium`. Non-streaming for this pass (simplicity over polish — a full response returned as JSON, not SSE). Response: `{answer, citations: [{file, startLine, endLine}]}`.

**3. Minimal chat UI at `cmd/api/web/index.html`**, embedded via `//go:embed` and served at `/` (replacing the current `/` → `/api/v1/me` redirect, which moves out of the way now that `/` is a real page). Vanilla HTML/JS, no framework, no build step: on load, call `/api/v1/me` — 401 shows a "Log in with GitHub" link to `/auth/github`; 200 shows the chat UI. Message list, textarea, send button, `fetch('/api/v1/chat', {credentials: 'include', ...})`, citations rendered as small `file:line` tags under each answer.

## Files

- `pkg/retrieval/` (new module: `go.mod`, `chunk.go`, `store.go`, `embedder.go`)
- `go.work` — add `./pkg/retrieval`
- `cmd/indexer/chunker.go`, `main.go`, `config.go` — updated imports; `store.go`/`embedder.go` deleted
- `cmd/api/lib/handlers/chat.go` (new)
- `cmd/api/main.go` — new config reads, construct `Store`/`Embedder`/Anthropic client, register `protected.POST("/api/v1/chat", ...)`, replace the `/` redirect with serving the embedded chat page
- `cmd/api/web/index.html` (new)
- `cmd/api/go.mod` — add `github.com/anthropics/anthropic-sdk-go`
- `cmd/api/config.local.example.yaml` (new) — documents the new keys (`database_url`, `embedding_provider`, matching embedding key, `anthropic_api_key`) alongside the existing GitHub/JWT ones, same spirit as the indexer's `.env.example`. Not touching your real `config.local.yaml` — you'll need to add these keys to it yourself with real values.

## Verification

- `go build ./...` across all modules (workspace-resolved via `go.work`, matching the existing setup).
- Run `cmd/api`, hit `/` unauthenticated → see the login link; log in via GitHub → see the chat UI.
- Ask a question about a file we know is indexed (e.g. `TwinStickCharacter.h`) and confirm the answer cites it correctly, and that the response doesn't invent citations for files that aren't in the 56 indexed.
- Check `response.usage.cache_read_input_tokens` on a second question in the same session to confirm the system-prompt cache is actually hitting.
