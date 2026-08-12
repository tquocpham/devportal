#!/usr/bin/env bash
# Run this ON THE PRODUCTION SERVER. Postgres runs only inside Docker on
# both machines, no host-level psql/pg_dump exists on either, so every
# database command below runs via `docker exec devportal-postgres`, using
# that container's own v16 client tools (matching both servers) instead of
# anything installed on the host. Pulls code_chunks from the dev server
# over Tailscale instead of re-running the indexer, no embedding API cost.
#
# Usage: DEV_HOST=100.x.y.z ./migrate_vectors.sh
set -euo pipefail

if [[ -z "${DEV_HOST:-}" ]]; then
  echo "Usage: DEV_HOST=<dev server's Tailscale IP or MagicDNS name> $0" >&2
  exit 1
fi
DEV_DATABASE_URL="postgres://postgres:postgres@${DEV_HOST}:5433/indexer?sslmode=disable"

# Local (prod's own container) connections use -U/-d against the default
# Unix socket, no host/port needed since psql/pg_dump run inside the same
# container Postgres is listening in. Remote (dev) connections need the
# full URL since we're reaching out over the network either way. -i is
# required on docker exec here, not optional: without it the container
# process's stdin isn't attached at all, so when this gets used as the
# receiving end of a pipe (step 4), psql sees closed/empty stdin,
# executes nothing, and exits 0 silently, no error, no output, exactly
# the "the restore did nothing" symptom this fixes.
PSQL_LOCAL=(docker exec -i devportal-postgres psql -U postgres -d indexer)

# 1. Confirm dev's Postgres is actually reachable from INSIDE this
#    container over Tailscale before committing to anything (reachable
#    from the host is a different question than reachable from inside
#    Docker's network namespace). Also captures dev's row count for the
#    final check below.
echo "Checking connectivity to dev (${DEV_HOST}) from inside the container..."
DEV_COUNT=$(docker exec devportal-postgres psql "$DEV_DATABASE_URL" -tAc "SELECT count(*) FROM code_chunks;")
echo "dev code_chunks row count: $DEV_COUNT"

# 2. Refuse to proceed if code_chunks already exists here — pg_dump's
#    plain-format CREATE TABLE has no IF NOT EXISTS and would fail loudly
#    anyway, but better to catch it early with a clear message.
EXISTS=$("${PSQL_LOCAL[@]}" -tAc "SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'code_chunks');")
if [[ "$EXISTS" == "t" ]]; then
  echo "code_chunks already exists on this server. Aborting to avoid a failed/partial restore." >&2
  exit 1
fi

# 3. pgvector extension must exist before the dump's CREATE TABLE
#    (embedding vector(1024)) can succeed. Pure DDL, no cost, no API calls,
#    same thing store.Migrate() does.
"${PSQL_LOCAL[@]}" -c "CREATE EXTENSION IF NOT EXISTS vector;"

# 4. Dump just code_chunks (schema + data + index + sequence) from dev,
#    restore directly into prod. Both sides run inside the same container,
#    for the same reason as step 1's connectivity check.
docker exec devportal-postgres pg_dump "$DEV_DATABASE_URL" -t code_chunks --no-owner --no-privileges \
  | "${PSQL_LOCAL[@]}"

# 5. Verify the counts actually match, not just that something landed.
PROD_COUNT=$("${PSQL_LOCAL[@]}" -tAc "SELECT count(*) FROM code_chunks;")
echo "prod code_chunks row count: $PROD_COUNT"
if [[ "$PROD_COUNT" != "$DEV_COUNT" ]]; then
  echo "WARNING: prod count ($PROD_COUNT) does not match dev count ($DEV_COUNT)." >&2
  exit 1
fi
echo "Done. $PROD_COUNT rows migrated."
