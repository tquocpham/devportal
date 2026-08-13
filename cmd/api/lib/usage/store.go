// Package usage tracks per-user, per-day chat question counts. Visibility
// only for now (docs/phase-4b-chat-usage-tracking-plan.md): Increment is
// called from ChatHandler.Chat on every request, Summary backs the
// admin-facing "who's using chat the most" view. No caps or blocking here,
// that's the separate, dependent docs/phase-6c-daily-question-cap-plan.md.
package usage

import (
	"database/sql"
	"time"
)

type store struct {
	db *sql.DB
}

type Store interface {
	CheckSchema() error
	Increment(username string) error
	AddTokens(username string, inputTokens, outputTokens int64) error
	TodayCount(username string) (int, error)
	Summary(periodStart, periodEnd time.Time) ([]UserSummary, error)
	ForUser(username string, periodStart, periodEnd time.Time) (UserSummary, error)
}

func NewStore(databaseURL string) (Store, error) {
	db, err := sql.Open("postgres", databaseURL)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		return nil, err
	}
	return &store{db: db}, nil
}

// CheckSchema fails fast if chat_usage hasn't been migrated yet, same
// pattern as users.Store.CheckSchema and retrieval.Store.CheckReady.
func (s *store) CheckSchema() error {
	var dummy int
	err := s.db.QueryRow(`SELECT 1 FROM chat_usage LIMIT 1`).Scan(&dummy)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	return nil
}

// Increment records one question for username today. Atomic upsert, no
// read-then-write race between concurrent requests from the same user.
func (s *store) Increment(username string) error {
	_, err := s.db.Exec(`
		INSERT INTO chat_usage (username, day, count)
		VALUES ($1, CURRENT_DATE, 1)
		ON CONFLICT (username, day) DO UPDATE SET count = chat_usage.count + 1
	`, username)
	return err
}

// AddTokens adds to username's token counts for today. Called separately
// from, and strictly after, Increment: token counts are only known once
// the model's response actually comes back, but a question should count as
// asked the moment it's received, regardless of whether generation
// succeeds. Safe to call as a plain UPDATE (no upsert needed) because
// Increment always runs first in the same request and guarantees today's
// row already exists.
func (s *store) AddTokens(username string, inputTokens, outputTokens int64) error {
	_, err := s.db.Exec(`
		UPDATE chat_usage
		SET input_tokens = input_tokens + $2, output_tokens = output_tokens + $3
		WHERE username = $1 AND day = CURRENT_DATE
	`, username, inputTokens, outputTokens)
	return err
}

// TodayCount returns username's question count for the current day, 0 if
// they haven't asked anything today. For Phase 6c's threshold check; not
// used by anything in this phase, added now since it's the same table and
// avoids a second migration later.
func (s *store) TodayCount(username string) (int, error) {
	var count int
	err := s.db.QueryRow(
		`SELECT count FROM chat_usage WHERE username = $1 AND day = CURRENT_DATE`,
		username,
	).Scan(&count)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return count, err
}

type UserSummary struct {
	Username     string    `json:"username"`
	Total        int       `json:"total"`
	InputTokens  int64     `json:"inputTokens"`
	OutputTokens int64     `json:"outputTokens"`
	LastDay      time.Time `json:"lastDay"`
}

// Summary returns total questions and token usage per user within
// [periodStart, periodEnd), most active first (by question count, not
// tokens, a small number of very long conversations shouldn't outrank a
// heavy day-to-day user in this ranking). The admin-facing "who's using
// chat" view, scoped to one billing period, see CurrentBillingPeriod.
// chat_usage itself keeps every day forever regardless, this only affects
// what this query returns, nothing is ever deleted to produce this scoping.
func (s *store) Summary(periodStart, periodEnd time.Time) ([]UserSummary, error) {
	rows, err := s.db.Query(`
		SELECT username, SUM(count) AS total,
		       SUM(input_tokens) AS input_tokens, SUM(output_tokens) AS output_tokens,
		       MAX(day) AS last_day
		FROM chat_usage
		WHERE day >= $1 AND day < $2
		GROUP BY username
		ORDER BY total DESC
	`, periodStart, periodEnd)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []UserSummary
	for rows.Next() {
		var u UserSummary
		if err := rows.Scan(&u.Username, &u.Total, &u.InputTokens, &u.OutputTokens, &u.LastDay); err != nil {
			return nil, err
		}
		list = append(list, u)
	}
	return list, rows.Err()
}

// ForUser returns one user's total questions and token usage within
// [periodStart, periodEnd). Unlike Summary, never returns an empty result
// for a user with no rows in the period, a zero-value UserSummary (0
// questions, 0 tokens) instead, so callers don't need a separate
// not-found case just to show "you haven't asked anything yet."
func (s *store) ForUser(username string, periodStart, periodEnd time.Time) (UserSummary, error) {
	u := UserSummary{Username: username}
	var lastDay sql.NullTime
	err := s.db.QueryRow(`
		SELECT COALESCE(SUM(count), 0), COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0), MAX(day)
		FROM chat_usage
		WHERE username = $1 AND day >= $2 AND day < $3
	`, username, periodStart, periodEnd).Scan(&u.Total, &u.InputTokens, &u.OutputTokens, &lastDay)
	if err != nil {
		return UserSummary{}, err
	}
	if lastDay.Valid {
		u.LastDay = lastDay.Time
	}
	return u, nil
}

// CurrentBillingPeriod returns the [start, end) window containing now, for
// a billing cycle that resets on startDay of each month (1 = calendar
// month). If today is on or after startDay, the period started this month;
// otherwise it started last month, time.Date normalizes month=0 back to
// December of the previous year on its own, no special-casing needed.
func CurrentBillingPeriod(startDay int, now time.Time) (start, end time.Time) {
	year, month, day := now.Date()
	if day >= startDay {
		start = time.Date(year, month, startDay, 0, 0, 0, 0, now.Location())
	} else {
		start = time.Date(year, month-1, startDay, 0, 0, 0, 0, now.Location())
	}
	end = start.AddDate(0, 1, 0)
	return start, end
}
