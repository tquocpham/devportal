CREATE TABLE IF NOT EXISTS allowed_users (
	username  TEXT PRIMARY KEY,
	added_at  TIMESTAMP DEFAULT NOW()
);
