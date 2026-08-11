# API

The web service designers and developers actually use: GitHub OAuth login, a chat UI backed by retrieval-augmented Claude (grounded in whatever `cmd/indexer` has indexed), and a Postgres-backed login allowlist with roles. Single embedded binary: the whole `web/` directory (`index.html`, `style.css`, `app.js`) is `//go:embed`ded and served via Echo's `StaticFS`, not from a separate static directory.

## Prerequisites

- **Postgres with `pgvector`**, already indexed by `cmd/indexer`; see [`cmd/indexer/README.md`](../indexer/README.md). `cmd/api` reads the same `code_chunks` table it wrote and fatals at startup if that table doesn't exist yet.
- **A GitHub OAuth App** (Settings > Developer settings > OAuth Apps) for login. Callback URL must match `callback_url` below exactly.
- **An Anthropic API key**: the one company-paid key that powers chat for every logged-in user.
- **A Voyage or OpenAI API key**: must match whichever provider `cmd/indexer` used to build the index; query embeddings have to come from the same model/dimension as what's stored.

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
