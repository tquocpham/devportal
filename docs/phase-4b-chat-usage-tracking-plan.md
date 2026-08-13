# Phase 4b: Chat usage tracking (visibility only, no caps yet)

**Status: shipped.** Grew during implementation beyond the original "just question counts" scope to include real token usage and billing-period scoping, both driven by follow-up asks once the basic tracking was in place. Updated here to reflect what actually exists, not just what was originally planned.

## Context

Prompted by asking whether devportal has any info on chat usage by user. It didn't: `chat.go`'s `Chat` handler never recorded who asked a question or how many, the request logger (`main.go`'s `LogValuesFunc`) only captured method/URI/status/latency (never `claims["sub"]`), and there was no table for it, only `code_chunks` (the index) and `allowed_users` (the login allowlist) existed.

Then, onboarding real users raised the actual goal directly: monitor token spend and eventually cap it before it gets expensive. That reframed this from "just visibility" to "visibility that's actually accurate enough to monitor real cost against a real invoice," which is why token counts and billing-period scoping both ended up in this phase rather than staying deferred to Phase 6c.

`docs/phase-6-cost-optimization-plan.md` (item C) / [`docs/phase-6c-daily-question-cap-plan.md`](phase-6c-daily-question-cap-plan.md) still owns the actual cap/block behavior and the graduation-to-MCP messaging, **not built here**. This phase is the foundation it depends on: the table, the per-user/per-period queries, and `TodayCount` (added, unused by anything yet) all exist specifically so 6c doesn't need its own migration.

## Approach

**Table** (`cmd/api/migrations/0003_create_chat_usage.sql`), day-level granularity so history is never lost to aggregation, and BIGINT token columns matching `anthropic.BetaUsage`'s own `int64` fields:

```sql
CREATE TABLE IF NOT EXISTS chat_usage (
    username      TEXT NOT NULL,
    day           DATE NOT NULL,
    count         INT NOT NULL DEFAULT 0,
    input_tokens  BIGINT NOT NULL DEFAULT 0,
    output_tokens BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (username, day)
);
```

**`cmd/api/lib/usage/store.go`**, same shape as `users.Store` (`NewStore`, `CheckSchema`):
- `Increment(username)` — atomic upsert on `(username, day)`, called at the *start* of `Chat`, before the tool loop and before the Anthropic call. **Fatal** if it errors: the whole point of this feature is knowing what's being spent, so a request that can't be tracked doesn't get to spend money either, and failing here is free since no tokens have been bought yet.
- `AddTokens(username, inputTokens, outputTokens)` — called separately, strictly *after* `Increment` and after the Anthropic call succeeds, once real usage is known. A plain `UPDATE`, not an upsert: safe because `Increment` already guarantees today's row exists. **Non-fatal** if it errors, deliberately the opposite of `Increment`: the money's already spent by this point, failing the request now would only additionally deny the user the answer they already paid for, with no upside.
- `TodayCount(username)` — for Phase 6c, unused here.
- `Summary(periodStart, periodEnd)` / `ForUser(username, periodStart, periodEnd)` — both scoped to a window, not all-time. `ForUser` never errors on "no rows," returns a zero-value result so callers don't need a separate not-found case.
- `CurrentBillingPeriod(startDay, now)` — resolves the `[start, end)` window for a billing cycle that resets on `startDay` of each month (`time.Date` normalizes `month=0` back to December on its own). Configurable via `billing_period_start_day` (default `1`, clamped to `[1, 28]` so it's valid in every month). History is never deleted, this only changes what a query's `WHERE` clause includes.

**Getting real token counts required a `chat.go` restructure**, not just reading `resp.Usage` off `RunToCompletion`'s return value. Verified directly against SDK source (`betatoolrunner.go`): `RunToCompletion` is a loop over `NextMessage`, and each `search_codebase` round-trip is a *separate* API call, but `RunToCompletion` only returns the *final* message, silently discarding every earlier round-trip's `Usage`. Any question needing 2+ searches would have undercounted. Fixed by driving `NextMessage` manually in `Chat` and summing `Usage.InputTokens`/`OutputTokens` across every iteration. One subtlety confirmed against `executeTools`'s source: the very last `NextMessage` call re-returns the same `*BetaMessage` pointer a second time purely to signal "done" (no new API call made), so accumulation compares pointer identity, not just non-nil, to avoid double-counting that final message.

**Admin endpoint**: `GET /api/v1/admin/chat-usage` → `AdminChatUsageHandler.List`, wraps `Summary()` with `{periodStart, periodEnd, users}` so the UI can show which window it's looking at.

**Self-service endpoint**: `GET /api/v1/me/chat-usage` → `ChatUsageHandler.Me`, same period scoping, `ForUser` instead of `Summary`, no admin required, so people can see their own usage without asking an admin.

**UI, two places**:
- Admin tab: "Chat usage" table (Username | Questions | Input tokens | Output tokens | Last asked) below the Users table, with the current billing-period range shown above it.
- Chat view itself: a small persistent indicator ("N questions · N,NNN tokens this billing period") above the message list, refreshed on tab-open and after every message (regardless of success/failure, since `Increment` always fires).

**Config**: `billing_period_start_day` (optional, default `1`). No other new config, reuses `database_url`.

## Files

- `cmd/api/migrations/0003_create_chat_usage.sql` (new)
- `cmd/api/lib/usage/store.go` (new) — `Store`, `NewStore`, `CheckSchema`, `Increment`, `AddTokens`, `TodayCount`, `Summary`, `ForUser`, `CurrentBillingPeriod`, `UserSummary`
- `cmd/api/lib/handlers/chat.go` — `usage` field, manual `NextMessage` loop replacing `RunToCompletion`, `Increment` + `AddTokens` calls
- `cmd/api/lib/handlers/admin_chat_usage.go` (new) — `AdminChatUsageHandler`, `List`
- `cmd/api/lib/handlers/chat_usage.go` (new) — `ChatUsageHandler`, `Me`
- `cmd/api/main.go` — construct `usage.NewStore`, `CheckSchema` fatal check, read `billing_period_start_day`, wire both handlers, register both routes
- `cmd/api/web/index.html` / `app.js` — admin Chat usage table, chat-view usage indicator

## Verification

All of the below were actually run, not just planned:

- `go build ./... && go vet ./... && go test ./...` — clean.
- Two `Increment` calls for the same user/day produce one row with `count=2`, not a duplicate, confirmed directly against dev's DB.
- `GET /api/v1/admin/chat-usage` and the period-window `SELECT` both confirmed against the real running server and real SQL, not just reasoned about.
- `ForUser`'s zero-row case and the period-boundary math (`CurrentBillingPeriod`) confirmed via direct query.
- The `RunToCompletion` → manual-loop fix was verified by reading `betatoolrunner.go`/`executeTools` source directly (not assumed), pending the user's own live multi-search chat test to confirm empirically.
- **Not yet empirically confirmed**: that a broken `usage_store` connection actually makes `POST /api/v1/chat` fail with `500` *before* reaching Anthropic (no cost incurred), and that the same failure *after* a successful response (simulated at the `AddTokens` call) still returns the answer to the user. Both were reasoned through carefully but not run against the live server yet.
