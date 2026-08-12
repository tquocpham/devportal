# Phase 4b: Chat usage tracking (visibility only)

## Context

Prompted by asking whether devportal has any info on chat usage by user. It doesn't: `chat.go`'s `Chat` handler never records who asked a question or how many, the request logger (`main.go`'s `LogValuesFunc`) only captures method/URI/status/latency (never `claims["sub"]`), and there's no table for it, only `code_chunks` (the index) and `allowed_users` (the login allowlist) exist today.

`docs/phase-6-cost-optimization-plan.md` (item C) already designs a `chat_usage` table, but bundles it with a hard daily cap and a graduation-to-MCP message. The narrower slice needed right now is **just the tracking plus an admin-visible view of who's asking how much**, no caps, no blocking, no graduation messaging. That fuller item C (and items A/B, the prompt-caching and dedup cost levers) stays exactly as already planned in that doc for later; this phase only builds the visibility piece, in a shape that doesn't need to be reworked when the cap is added on top later. Filed as its own phase doc (4b) rather than folded into Phase 6, since it ships standalone and well ahead of the rest of that phase.

## Approach

**New table**, migration-file-driven the same way `allowed_users` is (`cmd/api/migrations/0001_create_allowed_users.sql` is the only file there today; this adds `0002_create_chat_usage.sql`):

```sql
CREATE TABLE IF NOT EXISTS chat_usage (
    username TEXT NOT NULL,
    day      DATE NOT NULL,
    count    INT NOT NULL DEFAULT 0,
    PRIMARY KEY (username, day)
);
```

Day-level granularity (not just a running total) is stored from day one even though the UI below only shows an aggregate, so a later "usage over time" view or the Phase 6 daily cap never needs a schema change.

**New package `cmd/api/lib/usage/store.go`**, same shape as `cmd/api/lib/users/store.go` (`NewStore(databaseURL)`, `CheckSchema()` fatal-at-startup check, same pattern as `users.Store`/`retrieval.Store`):
- `Increment(username string) error` — `INSERT ... ON CONFLICT (username, day) DO UPDATE SET count = chat_usage.count + 1`, one atomic upsert, no read-then-write race.
- `Summary() ([]UserSummary, error)` — `SELECT username, SUM(count) AS total, MAX(day) AS last_day FROM chat_usage GROUP BY username ORDER BY total DESC`. This is the admin-facing "who's using chat the most" view.

**`chat.go` changes**: `ChatHandler` gets a `usage *usage.Store` field (threaded through `NewChatHandler`, same wiring style as `store`/`embedder`). At the top of `Chat`, after validating `req.Message` is non-empty, pull `username` from `c.Get("user").(jwt.MapClaims)["sub"]` (same access pattern already used in `Me`, `AssumeRole`, and the AWS handlers) and call `h.usage.Increment(username)`. **Non-fatal on error**, log it and continue into the chat flow rather than failing the request over a tracking write; usage tracking should never be able to break the actual feature. Placed before the tool loop starts (matching where Phase 6's future cap-check would go), so this doesn't need to move when that's added later.

**New admin endpoint**: `cmd/api/lib/handlers/admin_chat_usage.go`, `AdminChatUsageHandler.List` → `GET /api/v1/admin/chat-usage`, registered in `main.go` inside the existing `admin` group (already behind `RequireAdmin`, same as `/admin/users`). Returns `usage.Summary()` as JSON.

**UI**: a new "Chat usage" section in the Admin tab (`cmd/api/web/index.html`), directly below the existing Users table, same `table.users-table` styling, columns `Username | Questions | Last asked`, read-only (no actions needed for this scope). `app.js` gets a `loadChatUsage()`/`renderChatUsage()` pair mirroring `loadUsers()`/`renderUsers()` exactly, fetched when the Admin tab is shown (same `if (name === "admin")` hook in `showView` that already calls `loadUsers()`).

**Config**: none needed, reuses the existing `database_url`.

## Files

- `cmd/api/migrations/0002_create_chat_usage.sql` (new)
- `cmd/api/lib/usage/store.go` (new) — `Store`, `NewStore`, `CheckSchema`, `Increment`, `Summary`, `UserSummary`
- `cmd/api/lib/handlers/chat.go` — add `usage` field, wire `NewChatHandler`, increment call in `Chat`
- `cmd/api/lib/handlers/admin_chat_usage.go` (new) — `AdminChatUsageHandler`, `List`
- `cmd/api/main.go` — construct `usage.NewStore`, `CheckSchema` fatal check, pass into `NewChatHandler`, register `GET /api/v1/admin/chat-usage`
- `cmd/api/web/index.html` — Chat usage table in `#admin-view`
- `cmd/api/web/app.js` — `loadChatUsage`/`renderChatUsage`, hooked into the existing admin-tab-shown logic

## Verification

- `go build ./... && go vet ./... && go test ./...`, same bar as every other change this session.
- Run `cmd/api/migrations/migrate.sh` against the dev DB (or the same `docker exec ... psql` pattern used to verify prior migrations), confirm `chat_usage` exists.
- Ask a few chat questions as different users (or via `AssumeRole` to simulate a second identity), confirm `SELECT * FROM chat_usage` shows the right per-user, per-day counts incrementing correctly, including two questions on the same day incrementing the same row rather than creating a duplicate.
- Load the Admin tab in the browser, confirm the new Chat usage table shows real counts sorted by most-active-first.
- Confirm a chat request still succeeds even if `usage.Increment` is made to fail (e.g., temporarily point `usage.Store` at a bad connection), proving the non-fatal design actually holds.
