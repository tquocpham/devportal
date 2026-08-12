# Phase 6c: Generous per-user daily question cap, with a graduation path attached

Split out of `docs/phase-6-cost-optimization-plan.md`'s item C; see that doc for the shared cost-optimization context this belongs to. Safety net, not a quality lever, independent of Phase 6a/6b.

**Depends on [Phase 4b](phase-4b-chat-usage-tracking-plan.md)**, not just Phase 4: 4b already builds the `chat_usage` table, the `usage.Store` package (`Increment`/`Summary`), and wires `Increment` into `ChatHandler.Chat`. This phase does **not** re-specify any of that, it only adds the threshold check on top of the counter 4b already maintains.

## Context

**Problem**: nothing currently bounds aggregate spend across all users. `chatMaxIterations` only bounds cost *per question*. The only aggregate backstop today is the Anthropic Console's account-wide spend limit, which, if it ever trips, takes down the app for *everyone*, not just whoever's responsible.

**The intended product shape**: the company-paid web chat is the default, free-to-the-user tier, but it isn't meant to be unlimited. A user who consistently exceeds it is expected to graduate to bringing their own Claude account, and from that point on uses the codebase via the Phase 4 MCP server instead of this app's chat UI. Critically, that handoff is meant to be a *success path*, not a cutoff, someone who hits the cap should be pointed at exactly how to keep going effectively on their own account, not just told no. Phase 4 (MCP server) already shipped ahead of this phase specifically so that's already true by the time this lands.

## Approach

Add the threshold check to `ChatHandler.Chat`, reading (not just incrementing) `usage.Store` before the tool loop starts, against a configurable daily limit (`chat_daily_question_limit`). Two limits, not one, is closer to the actual intent:

- A **soft threshold** (e.g. `chat_daily_soft_limit`, default ~30), the response still succeeds, but includes a note-to-self-style nudge: "you're a heavy user of the shared plan, see the Claude Code tab for how to keep going on your own account." Informational only, never blocks.
- A **hard cap** (`chat_daily_question_limit`, default e.g. 50), the response is rejected, but the error is specific and actionable, not a generic "rate limited": it explains the shared-plan cap was hit for the day, and points at the MCP setup path (the Claude Code tab, built in Phase 4) as the way to keep working today rather than waiting until tomorrow. This is the literal implementation of "set them up for success" once they've outgrown the free tier, the cap hands them somewhere to go, it doesn't just stop them.

Both numbers should stay high enough that they essentially never bind for someone using this as intended (occasional-to-regular questions); they exist to catch the tail, a stuck loop, a bug, or someone who's genuinely outgrown the shared plan and would legitimately benefit from their own account.

`usage.Store` (from Phase 4b) needs one addition beyond what 4b already built: a same-day count read, either exposed directly (`Summary` already returns `MAX(day)` per user but not "today's count" specifically) or a small `TodayCount(username string) (int, error)` method, `SELECT count FROM chat_usage WHERE username = $1 AND day = CURRENT_DATE`.

## Files

- `cmd/api/lib/usage/store.go` (from Phase 4b) — add `TodayCount`
- `cmd/api/lib/handlers/chat.go` — threshold check before the tool loop, soft-threshold note attached to successful responses past the soft limit, hard-cap rejection with the MCP-setup message
- `cmd/api/config.local.example.yaml` — new `chat_daily_soft_limit`/`chat_daily_question_limit` keys, following the existing `chat_*` config pattern in `DefaultChatConfig`

## Verification

Temporarily set `chat_daily_soft_limit`/`chat_daily_question_limit` low (e.g. 2/4), confirm questions past the soft threshold still succeed but carry the graduation nudge, confirm the question past the hard cap is rejected with the specific MCP-setup message (not a generic error), and confirm a new day resets the count (proving the `day` column, not a rolling window, is what's actually checked).
