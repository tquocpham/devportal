-- One-time bootstrap: grants the first admin access so someone can log in
-- and start granting/revoking everyone else. Run once per environment,
-- separately from migrations/migrate.sh — schema setup and admin access are
-- different CI/CD processes (see cmd/api/README.md). Lives in this
-- seed/ subdirectory specifically so migrate.sh's flat `*.sql` glob over its
-- own directory doesn't pick it up and re-run it on every schema change.
--
-- Usage: psql "$DATABASE_URL" -f cmd/api/migrations/seed/seed-admin.sql
--
-- Uses DO UPDATE (not DO NOTHING) so re-running this always leaves the
-- bootstrap user as admin, even if they were previously granted as a plain
-- developer some other way.
INSERT INTO allowed_users (username, role) VALUES ('tquocpham', 'admin')
	ON CONFLICT (username) DO UPDATE SET role = 'admin';
