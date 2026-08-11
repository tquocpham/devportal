# Phase 6: Chat cost optimization

## Context

Designers are expected to ask a lot of questions — that's the actual goal of this tool (guiding them to better code, not just occasional lookups), which means per-conversation cost adds up faster than originally budgeted for. Explicit non-goal for this phase: don't reduce answer quality to save money — no cheaper model, no lower `chat_max_iterations`/`chat_top_k`. Those are real levers but they trade away the thing that matters most for a coaching tool. Everything here is structural efficiency: same answers, less wasted spend.

**The intended product shape, now clarified**: the company-paid web chat is the default, free-to-the-user tier — but it isn't meant to be unlimited. A user who consistently exceeds it is expected to graduate to bringing their own Claude account, and from that point on uses the codebase via the Phase 4 MCP server instead of this app's chat UI. Critically, that handoff is meant to be a *success path*, not a cutoff — someone who hits the cap should be pointed at exactly how to keep going effectively on their own account, not just told no. That reframes item C below from a bare rate limit into an onboarding moment for Phase 4, which now ships ahead of this phase specifically so that's already true by the time this lands.

Three independent, additive changes, ranked by leverage:

## A. Incremental prompt caching in the tool loop (biggest lever)

**Problem**: only the system prompt is cached today (`chat.go`'s `CacheControl` on the system block). Every iteration of the search loop (up to `chatCfg.MaxIterations`) re-sends and re-prices the *entire* growing conversation — history, the question, and every prior search result — as fresh input tokens, even though most of it is identical to the previous iteration.

**Fix**: switch `ChatHandler.Chat` from `runner.RunToCompletion(ctx)` to driving the loop manually via repeated `runner.NextMessage(ctx)` calls, moving a cache breakpoint to the end of the message list after each one. Confirmed against the installed SDK source (`betatoolrunner.go`), not assumed:

- `BetaToolRunner` embeds `betaToolRunnerBase`, whose `Params BetaToolRunnerParams` field is exported specifically so callers can inspect/mutate it (`betatoolrunner.go:39`, doc comment says as much).
- `NextMessage` reads `r.Params.Messages` fresh at the top of every call (`betatoolrunner.go:333-368`) — so mutating `runner.Params.Messages` between calls is a supported pattern, not reaching into internals.
- Both `BetaTextBlockParam` and `BetaToolResultBlockParam` have a `CacheControl BetaCacheControlEphemeralParam` field (`betamessage.go:8583-8591` and `:10251-10260`) — so a breakpoint can be set on whichever block type ends up last, whether that's the model's own text or a tool result `executeTools` appended.

Loop shape:

```go
var lastMsg *anthropic.BetaMessage
for {
    msg, err := runner.NextMessage(ctx)
    if err != nil { ... }
    if msg == nil {
        break // conversation complete
    }
    lastMsg = msg
    moveCacheBreakpoint(runner) // sets CacheControl on the last block of runner.Params.Messages, clearing whichever block we marked last iteration
}
answer := extractText(lastMsg)
```

**Must verify at implementation time, not assumed here**: the current per-request cache-breakpoint limit (moving exactly one breakpoint forward each iteration, rather than stacking a new one every turn, is the safe approach regardless of the exact limit, but confirm against live Anthropic docs before writing the final version — this project's existing practice is to never guess API behavior that might have drifted).

**Files**: `cmd/api/lib/handlers/chat.go`. Once Phase 5 (code review) exists, `review.go` should reuse the same loop helper rather than duplicating it — worth extracting into `searchtool.go` alongside the already-shared `newSearchCodebaseTool`.

## B. Dedup tool-result content within a single turn (small, free win)

**Problem**: `search_codebase`'s citation dedup (the `seen` map already in `chat.go`) stops the same `file:line` from being *listed twice* in the response, but doesn't stop the full chunk *text* from being sent to the model twice if two searches in the same turn return overlapping chunks.

**Fix**: reuse that same `seen` map when building each search call's result text — for a chunk already returned earlier in this turn, replace the full body with a one-line "(already shown above)" marker instead of repeating it.

**Files**: `cmd/api/lib/handlers/chat.go` (the `search_codebase` tool closure) — same future home as (A) once extracted to `searchtool.go`.

## C. Generous per-user daily question cap, with a graduation path attached (safety net, not a quality lever)

**Problem**: nothing currently bounds aggregate spend across all users. `chatMaxIterations` only bounds cost *per question*. The only aggregate backstop today is the Anthropic Console's account-wide spend limit, which — if it ever trips — takes down the app for *everyone*, not just whoever's responsible.

**Fix**: a small Postgres-backed daily counter, checked and incremented in `ChatHandler.Chat` before the tool loop starts, against a configurable daily limit (`chat_daily_question_limit`) — deliberately generous, not a rationing tool. Two limits, not one, is closer to the actual intent:

- A **soft threshold** (e.g. `chat_daily_soft_limit`, default ~30) — the response still succeeds, but includes a note-to-self-style nudge: "you're a heavy user of the shared plan — see `/mcp-setup` for how to keep going on your own account." Informational only, never blocks.
- A **hard cap** (`chat_daily_question_limit`, default e.g. 50) — the response is rejected, but the error is specific and actionable, not a generic "rate limited": it explains the shared-plan cap was hit for the day, and points at the MCP setup path (`cmd/api/README.md`'s MCP section, built in Phase 4) as the way to keep working today rather than waiting until tomorrow. This is the literal implementation of "set them up for success" once they've outgrown the free tier — the cap hands them somewhere to go, it doesn't just stop them.

Both numbers should stay high enough that they essentially never bind for someone using this as intended (occasional-to-regular questions); they exist to catch the tail — a stuck loop, a bug, or someone who's genuinely outgrown the shared plan and would legitimately benefit from their own account.

New table, migration-file-driven the same way `allowed_users` is:

```sql
CREATE TABLE IF NOT EXISTS chat_usage (
    username TEXT NOT NULL,
    day      DATE NOT NULL,
    count    INT NOT NULL DEFAULT 0,
    PRIMARY KEY (username, day)
);
```

**Files**: new `cmd/api/lib/usage/store.go` (same shape as `users.Store` — `NewStore`, an atomic increment-and-check method), new `cmd/api/migrations/000X_create_chat_usage.sql`, `chat.go` (check-and-increment before constructing the tool runner, soft-threshold note attached to successful responses past the soft limit), `config.local.example.yaml` (new `chat_daily_soft_limit`/`chat_daily_question_limit` keys, following the existing `chat_*` config pattern in `DefaultChatConfig`). **Depends on Phase 4 (MCP server)** for the graduation message to point somewhere real — no longer a forward reference now that MCP is queued ahead of this phase, so by the time this is built, Phase 4's setup docs already exist to link to.

## Verification

- **A**: ask a question that triggers 2+ searches; check `response.usage.cache_read_input_tokens` on the later iterations — same technique already used in Phase 2 to confirm system-prompt caching was hitting, now extended to the growing conversation.
- **B**: craft two search queries in one turn likely to return overlapping chunks; confirm the second occurrence is replaced with the "already shown" marker rather than full text.
- **C**: temporarily set `chat_daily_soft_limit`/`chat_daily_question_limit` low (e.g. 2/4), confirm questions past the soft threshold still succeed but carry the graduation nudge, confirm the question past the hard cap is rejected with the specific MCP-setup message (not a generic error), and confirm a new day resets the count.
