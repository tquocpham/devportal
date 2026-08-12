# API

The web service designers and developers actually use: GitHub OAuth login, a chat UI backed by retrieval-augmented Claude (grounded in whatever `cmd/indexer` has indexed), self-service AWS access (LFS credentials, console login, temporary ops credentials), and a Postgres-backed login allowlist with roles. Single embedded binary: the whole `web/` directory (`index.html`, `style.css`, `app.js`) is `//go:embed`ded and served via Echo's `StaticFS`, not from a separate static directory.

## Prerequisites

- **Postgres with `pgvector`**, already indexed by `cmd/indexer`; see [`cmd/indexer/README.md`](../indexer/README.md). `cmd/api` reads the same `code_chunks` table it wrote and fatals at startup if that table doesn't exist yet.
- **A GitHub OAuth App** (Settings > Developer settings > OAuth Apps) for login. Callback URL must match `callback_url` below exactly.
- **An Anthropic API key**: the one company-paid key that powers chat for every logged-in user.
- **A Voyage or OpenAI API key**: must match whichever provider `cmd/indexer` used to build the index; query embeddings have to come from the same model/dimension as what's stored.
- **AWS one-time setup**: a provisioner IAM user, the two bucket-scoped IAM policies (or let the app create them on first use), and an STS role. See [`docs/aws-one-time-setup.md`](../../docs/aws-one-time-setup.md); `cmd/api` fatals at startup if these credentials aren't valid (`sts:GetCallerIdentity` is checked immediately).

## Setup

Copy `config.local.example.yaml` to `config.local.yaml` (gitignored, never commit it) and fill in real values. See that file for the full list of keys and what's optional.

### Database setup

Schema and access-control setup are deliberately **not** something `cmd/api` does on startup; it only checks the schema exists and fatals with a clear message if it doesn't. Run these from CI/CD (or by hand) before the service starts, in order:

1. **Schema migrations**: run once per environment, and again whenever `cmd/api/migrations/` changes:

   ```bash
   DATABASE_URL="$DATABASE_URL" cmd/api/migrations/migrate.sh
   ```

   Runs every `.sql` file directly under `cmd/api/migrations/`, in filename order. Idempotent (`CREATE TABLE IF NOT EXISTS`, etc.); safe to re-run.

2. **Bootstrap the first admin**: the login allowlist fails closed (an empty table means **nobody** can log in, not "no restriction configured"), so run this once per environment, separately from `migrate.sh`:

   ```bash
   psql "$DATABASE_URL" -f cmd/api/migrations/seed/seed-admin.sql
   ```

   Grants the one hardcoded admin username in that file (currently `tquocpham`) so someone can log in and start granting everyone else. It lives in `migrations/seed/` specifically so `migrate.sh`'s glob over its own directory doesn't pick it up and re-run it as part of routine schema changes; it's a one-time step, not a repeating migration.

### Access control (roles)

Every allowed user has a `role`: `admin` or `developer` (`users.Role` in `cmd/api/lib/users/store.go`, enforced in Postgres too via a CHECK constraint). `developer` just gets to use the platform. `admin` is meant for anyone who can also manage *other* users' access (and whatever else ends up admin-gated later); there's no admin API yet, so today that only means "can run the commands below," but the role is already carried through login into the session JWT (`claims["role"]`, also returned by `/api/v1/me`) so a future admin-only endpoint can check it without a schema change.

Admins add and remove users on an ongoing basis, independent of any deploy; this is a separate CI/CD job (or a one-off command) from schema setup, not something bundled into a deploy step:

```bash
# grant as developer (default role, omitting the column does the same thing)
psql "$DATABASE_URL" -c "INSERT INTO allowed_users (username, role) VALUES ('some-github-username', 'developer') ON CONFLICT (username) DO NOTHING;"

# grant as admin
psql "$DATABASE_URL" -c "INSERT INTO allowed_users (username, role) VALUES ('some-github-username', 'admin') ON CONFLICT (username) DO NOTHING;"

# change an existing user's role
psql "$DATABASE_URL" -c "UPDATE allowed_users SET role = 'admin' WHERE username = 'some-github-username';"

# revoke entirely
psql "$DATABASE_URL" -c "DELETE FROM allowed_users WHERE username = 'some-github-username';"
```

`username` must be the person's exact GitHub login. `role` must be `admin` or `developer`; the CHECK constraint rejects anything else.

### AWS access

Three self-service flows, all under the AWS Access tab in the UI (design/rationale: [`docs/phase-3-aws-access-plan.md`](../../docs/phase-3-aws-access-plan.md)):

- **`POST /api/v1/aws/lfs-access-key`** / **`DELETE /api/v1/aws/lfs-access-key`**: a long-lived access key scoped to just the LFS bucket, issued directly, no console visit. Capped at one active key per person; delete-then-recreate is the rotation path. Meant for git/LFS, set once and forgotten.
- **`POST /api/v1/aws/console-access`**: one-time AWS console login (username + temp password), for devops work that actually needs the browser console (S3 browsing, CloudWatch). Not needed just for git/LFS.
- **`POST /api/v1/aws/sts-credentials`**: short-lived `sts:AssumeRole` credentials for ad hoc ops work; nothing long-lived is ever created.

All three derive the IAM identity from the caller's own GitHub username (same session JWT as everything else), and provision the underlying IAM user/policies idempotently on first use, no manual per-person setup beyond the one-time AWS setup above.

Admins can also revoke someone else's console login directly from the Admin tab's users table: **`DELETE /api/v1/admin/users/:username/aws-console-access`** removes their IAM login profile outright (e.g. as part of offboarding), which the self-service flow above deliberately can't do (Flow A2 only ever creates one, never rotates or removes it).

## Running it

From `cmd/api/`:

```bash
go run .
```

Serves on `:$port` (default `3000`). Visit `http://localhost:3000`: unauthenticated requests see a "Log in with GitHub" link; after login, the chat UI.

## Verifying it worked

- `curl http://localhost:3000/api/v1/me --cookie "session=..."` (or just check in-browser after logging in); should return your GitHub identity plus `role`.
- Ask the chat a question about a file you know is indexed and confirm the citations look right.
- Check `SELECT username, role FROM allowed_users;` against the DB matches who you've actually granted.
- From the AWS Access tab, get an LFS access key, confirm it works (`aws s3 ls s3://<bucket>` with it) but can't do anything else (`aws iam list-users` should fail); delete it and confirm a fresh one issues cleanly.
