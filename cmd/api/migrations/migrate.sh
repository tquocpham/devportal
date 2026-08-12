#!/usr/bin/env bash
# Runs every .sql file in this directory, in filename order, against
# $DATABASE_URL. Intended to be run from CI/CD (or by hand) before deploying
# cmd/api. The service itself only checks the schema exists at startup, it
# never migrates itself. Safe to re-run: each file is idempotent
# (CREATE TABLE IF NOT EXISTS, etc.).
#
# Usage: DATABASE_URL=postgres://... ./migrate.sh
set -euo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

if [[ -z "${DATABASE_URL:-}" ]]; then
  echo "DATABASE_URL is required" >&2
  exit 1
fi

for f in "$DIR"/*.sql; do
  echo "Running $(basename "$f")..."
  psql "$DATABASE_URL" -f "$f"
done

echo "Done."
