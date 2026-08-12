# Semi-automatic dev environment: spin up local stack + pull prod data down

## Context

Reverse direction of the one-off prod migration script from earlier: instead of pushing dev's `code_chunks` up to bootstrap prod, this is a repeatable, on-demand way to pull *current* prod `code_chunks` down into a fresh local dev stack, so working on devportal locally never requires paying to re-run the indexer just to have something to search against.

**Scope, deliberately narrow**: `code_chunks` only, same as the prod-push script. Two other tables deliberately excluded, not forgotten:
- `allowed_users`: real GitHub usernames/roles for actual teammates. Keeping dev seeded independently (the existing `seed-admin.sql` / README raw-SQL flow) is already cheap and works today; syncing real user data down to every developer's disposable local machine adds either sync complexity or minor data-hygiene surface for no real benefit. A developer just needs *themselves* granted locally, not the whole team's real membership.
- `chat_usage` (Phase 4b) / `chat_answer_cache` (Phase 6d, backlogged): operational telemetry about real users, not something a developer needs locally.
- **Secrets/config are never synced.** `config.local.yaml` stays per-environment; pulling prod's API keys/credentials down to a dev machine would expand the blast radius of a dev-machine compromise to include prod credentials, the opposite of what this feature should do.

**Why the safety posture is inverted from the prod-push script**: that script aborted if the target already had a `code_chunks` table, because the target was production, overwriting it by accident would be a real incident. Here the target is disposable local dev data, so the right default is the opposite: always drop and replace with a fresh pull, no "abort if exists" guard, this needs to be safely re-runnable any time a developer wants current data, not a one-time careful operation.

**"Automatic or semi-automatic," recommending semi-automatic**: a single script a developer runs deliberately (`./scripts/sync-dev-data.sh`), not something wired into `docker compose up` itself. Auto-triggering a network-dependent sync against prod on every stack start would slow down routine restarts and could surprise someone who doesn't want their local data touched right that moment (mid-test, offline, etc.). "One clear command, run when you want it" fits "semi-automatic" better than fully-automatic here.

## Approach

**New top-level `scripts/` directory** (doesn't exist yet), since this operates across the whole local stack (`docker-compose.yml` at the repo root), not one `cmd/*` binary specifically.

**`scripts/sync-dev-data.sh`**:
1. Require `PROD_DATABASE_URL` as an environment variable, **not hardcoded in the script**. The prod-push script's IP/credentials were fine to paste directly into a one-off command run by hand; a *committed* script is a different story, don't bake a prod connection string (even a Tailscale-private one) into git history. Fail immediately with a clear message if it's unset, don't silently no-op.
2. `docker compose up -d postgres`, then wait for it healthy (`docker compose ps` / the existing healthcheck), **not** the full stack yet. `cmd/api` would crash-loop on startup if brought up before `code_chunks` exists (`store.CheckReady()` fatals), same ordering constraint `docs/deploy-tailscale.md` already documents for a fresh deploy, just automated here instead of manual steps.
3. `psql "$LOCAL_DB" -c "CREATE EXTENSION IF NOT EXISTS vector;"` (pure DDL, no cost, same as the prod-push script's step 1).
4. `psql "$LOCAL_DB" -c "DROP TABLE IF EXISTS code_chunks;"` then the same `pg_dump ... -t code_chunks --no-owner --no-privileges | psql "$LOCAL_DB"` restore pattern as the prod-push script, just source and target swapped.
5. Verify: compare row counts between `$PROD_DATABASE_URL` and local, same pattern as the prod-push script's step 5, print a clear mismatch warning rather than silently succeeding on a partial transfer.
6. `docker compose up -d --build api`, now that `code_chunks` actually exists, this is the point where bringing `api` up (or back up, if it was already running and crash-looping) is actually safe.

**Where `PROD_DATABASE_URL` comes from, developer-side**: export it in a shell profile, or (matching this project's existing gitignored-local-file convention) a `.env.local` sourced at the top of the script if present, `.env.local` is already in `.gitignore`. Not solving this with a checked-in file either way, same reasoning as why `config.local.yaml` is gitignored.

**Re-runnable by design**: running this a second time (developer wants fresh data a week later) just drops and re-pulls, no special "first time vs. subsequent" branching needed, the drop-and-replace step handles both cases identically.

## Files

- `scripts/sync-dev-data.sh` (new)
- `cmd/api/README.md` — add this as the recommended alternative to running `cmd/indexer` locally, in whichever section currently tells a new developer to index the codebase themselves
- `.gitignore` — confirm `.env.local` coverage already applies at the repo root too if the script sources one there, not just under `cmd/indexer/` where it's used today

## Verification

- Fresh clone, no local stack running yet: run the script, confirm `docker compose up -d postgres` → data pull → `api` startup all happen in the right order with no manual intervention beyond `export PROD_DATABASE_URL=...` first.
- Run it a second time immediately after: confirm it cleanly drops and re-pulls rather than erroring on "table already exists" (the exact case the prod-push script deliberately guards against, inverted here).
- Unset `PROD_DATABASE_URL` and run it: confirm a clear, immediate failure, not a confusing downstream error from `pg_dump` given an empty connection string.
- Confirm `cmd/api` actually starts successfully afterward and a real chat question returns results, proving the data that landed is actually usable, not just "some rows exist."
