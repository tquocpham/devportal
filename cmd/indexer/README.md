# Indexer

Walks a codebase, chunks source files on function/class boundaries, embeds each chunk, and stores the vectors in Postgres (`pgvector`) for semantic code search.

## Prerequisites

- **Postgres with `pgvector`**, running locally via Docker. A `docker-compose.yml` for this is already at the repo root:

  ```bash
  cd /Users/timmonpham/go/src/github.com/devportal
  docker compose up -d
  ```

  This starts a `pgvector/pgvector:pg16` container on **host port `5433`** (not `5432`: that port is already used locally by an unrelated project's plain, non-pgvector Postgres container). Confirm it's healthy:

  ```bash
  docker compose ps
  ```

- **A Voyage AI API key** (embeddings, free tier, `voyage-code-3`). Get one at [dash.voyageai.com](https://dash.voyageai.com).

## Setup

Create `cmd/indexer/.env.local` (already gitignored, never commit this file):

```bash
VOYAGE_API_KEY=pa-...
EMBEDDING_PROVIDER=voyage
PROJECT_PATH=/absolute/path/to/the/codebase/you/want/to/index
DATABASE_URL=postgres://postgres:postgres@localhost:5433/indexer?sslmode=disable
```

`sslmode=disable` matters here: `lib/pq` requires SSL by default, and the local Docker container doesn't have it configured. Omitting this gives `pq: SSL is not enabled on the server`.

`.env.example` in this directory has the same template with comments, if you'd rather copy it.

## Running it

From `cmd/indexer/`:

```bash
# First run: indexes everything under PROJECT_PATH
go run . -full

# After that: only re-indexes files changed since the last commit
go run . -changed
```

- `-full` clears the whole index first, then re-embeds every indexable file (`.cpp`, `.h`, `.cs`, `.ini` by default; see `Extensions` in `config.go`). Use it for the first run, or after a large restructuring.
- `-changed` only touches files from `git diff --name-only HEAD~1 HEAD` in `PROJECT_PATH`: cheap, but only correct if `PROJECT_PATH` is checked out to `main` and you run it right after each merge (see the architecture doc for why; it isn't a general "what's changed since I last indexed" diff).

## Verifying it worked

```bash
docker exec devportal-postgres psql -U postgres -d indexer -c \
  "SELECT rel_path, start_line, end_line, embedding IS NOT NULL AS has_embedding FROM code_chunks LIMIT 20;"
```

You should see real file paths with `has_embedding = t`. If the table's empty, check the `go run` output for errors: most per-chunk failures (a bad chunk, a transient network error) just log and skip, but the run stops outright (`log.Fatalf`) on:

- **Rate limiting (HTTP 429)**: Voyage caps you at 3 requests/min until you add a payment method at [dashboard.voyageai.com](https://dashboard.voyageai.com); the free 200M tokens/month still apply either way, this only affects request rate. Add a payment method, then re-run.
- **Hitting the embedding token budget** (`MAX_EMBEDDING_TOKENS`, see below): stops before you'd exceed the free tier, rather than silently going over.

The final `Done.` log line reports total tokens used for the run; check that against [dashboard.voyageai.com](https://dashboard.voyageai.com)'s usage page to track your monthly total across runs (the budget below only guards a single run, not cumulative usage across many runs in the same month).

### Token budget

`MAX_EMBEDDING_TOKENS` (optional, in `.env.local`) stops the run once cumulative embedding tokens reach that number. Defaults to just under Voyage's 200M/month free tier (`voyage` provider) or unlimited (`openai`, since it has no free tier to guard against). Override it explicitly if you want a tighter cap or know your plan differs.

## Switching embedding providers

`EMBEDDING_PROVIDER` can be `voyage` (default) or `openai`. **Switching after you've already indexed requires a full re-index**: different models produce incompatible vector spaces, and the `pgvector` column has a fixed dimension (`EmbeddingDim` in `embedder.go`, currently `1024` for Voyage). If you switch, also drop the table first:

```bash
docker exec devportal-postgres psql -U postgres -d indexer -c "DROP TABLE IF EXISTS code_chunks;"
```

then run `-full` again; `store.Migrate()` will recreate it with the right dimension.
