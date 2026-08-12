CREATE TABLE IF NOT EXISTS allowed_users (
	username  TEXT PRIMARY KEY,
	added_at  TIMESTAMP DEFAULT NOW(),
	role      TEXT NOT NULL DEFAULT 'developer'
		CHECK (role IN ('admin', 'developer'))
);
