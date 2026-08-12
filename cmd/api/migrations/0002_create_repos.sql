-- Catalog of known project repos. Separate from user_repo_access below so
-- an admin adds a repo once and grants it to many users, rather than
-- re-typing name/url on every grant.
CREATE TABLE IF NOT EXISTS repos (
	id         SERIAL PRIMARY KEY,
	name       TEXT NOT NULL,
	url        TEXT NOT NULL UNIQUE,
	created_at TIMESTAMP DEFAULT NOW()
);

-- Per-user repo access. Repo links on the home page are scoped to whichever
-- rows exist here for the logged-in user, not a global list everyone sees.
-- Both foreign keys cascade on delete: revoking a user (removed from
-- allowed_users) or retiring a repo (removed from repos) cleans up its
-- grants automatically instead of leaving orphaned rows behind.
CREATE TABLE IF NOT EXISTS user_repo_access (
	username   TEXT NOT NULL REFERENCES allowed_users (username) ON DELETE CASCADE,
	repo_id    INT NOT NULL REFERENCES repos (id) ON DELETE CASCADE,
	granted_at TIMESTAMP DEFAULT NOW(),
	PRIMARY KEY (username, repo_id)
);

-- For "which repos does this user see" lookups, the actual home-page query.
CREATE INDEX IF NOT EXISTS user_repo_access_username_idx
	ON user_repo_access (username);
