-- Per-user, per-day question counts and token usage. Day-level granularity
-- (not just a running total) is stored from day one even though the admin
-- UI only shows an aggregate, so a later "usage over time" view or the
-- Phase 6c daily cap never needs a schema change. BIGINT for the token
-- columns, not INT: matches anthropic.BetaUsage.InputTokens/OutputTokens's
-- own int64 type in the SDK, cheap to just match rather than reason about
-- whether INT's ~2.1B ceiling could ever matter.
CREATE TABLE IF NOT EXISTS chat_usage (
	username      TEXT NOT NULL,
	day           DATE NOT NULL,
	count         INT NOT NULL DEFAULT 0,
	input_tokens  BIGINT NOT NULL DEFAULT 0,
	output_tokens BIGINT NOT NULL DEFAULT 0,
	PRIMARY KEY (username, day)
);
