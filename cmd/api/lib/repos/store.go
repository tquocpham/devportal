// Package repos backs the per-user repo link feature: a catalog of known
// project repos (Store.List/Create/Delete) and, separately, which specific
// users can see which specific repos (Store.Grant/Revoke/ForUser). Schema
// lives in cmd/api/migrations/0002_create_repos.sql. Same store-per-concern
// shape as cmd/api/lib/users.Store, not merged into it since "who can log
// in" and "which repos they see" are different concerns that happen to
// share a database.
package repos

import (
	"database/sql"
	"errors"
	"time"

	"github.com/lib/pq"
)

var (
	ErrRepoExists   = errors.New("repo already exists")
	ErrRepoNotFound = errors.New("repo not found")
)

type Repo struct {
	ID        int       `json:"id"`
	Name      string    `json:"name"`
	URL       string    `json:"url"`
	CreatedAt time.Time `json:"createdAt"`
}

type Store struct {
	db *sql.DB
}

func NewStore(databaseURL string) (*Store, error) {
	db, err := sql.Open("postgres", databaseURL)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

// CheckSchema fails fast if repos/user_repo_access haven't been migrated
// yet, same pattern as users.Store.CheckSchema.
func (s *Store) CheckSchema() error {
	var dummy int
	err := s.db.QueryRow(`SELECT 1 FROM repos LIMIT 1`).Scan(&dummy)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	return nil
}

// List returns the full repo catalog, ordered by name. Admin-facing, not
// scoped to any one user.
func (s *Store) List() ([]Repo, error) {
	rows, err := s.db.Query(`SELECT id, name, url, created_at FROM repos ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRepos(rows)
}

// ForUser returns the repos a specific user has been granted, ordered by
// name. This is what the home page's repo list actually shows, scoped to
// whoever's logged in, not the full catalog.
func (s *Store) ForUser(username string) ([]Repo, error) {
	rows, err := s.db.Query(`
		SELECT r.id, r.name, r.url, r.created_at
		FROM repos r
		JOIN user_repo_access a ON a.repo_id = r.id
		WHERE a.username = $1
		ORDER BY r.name
	`, username)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRepos(rows)
}

func scanRepos(rows *sql.Rows) ([]Repo, error) {
	var list []Repo
	for rows.Next() {
		var r Repo
		if err := rows.Scan(&r.ID, &r.Name, &r.URL, &r.CreatedAt); err != nil {
			return nil, err
		}
		list = append(list, r)
	}
	return list, rows.Err()
}

// Create adds a repo to the catalog. ErrRepoExists if the URL is already
// registered (repos.url is UNIQUE).
func (s *Store) Create(name, url string) (Repo, error) {
	var r Repo
	err := s.db.QueryRow(
		`INSERT INTO repos (name, url) VALUES ($1, $2) RETURNING id, name, url, created_at`,
		name, url,
	).Scan(&r.ID, &r.Name, &r.URL, &r.CreatedAt)
	var pqErr *pq.Error
	if errors.As(err, &pqErr) && pqErr.Code == "23505" { // unique_violation
		return Repo{}, ErrRepoExists
	}
	return r, err
}

// Delete removes a repo from the catalog entirely. Cascades to remove every
// user's grant for it too (ON DELETE CASCADE on user_repo_access.repo_id),
// not just this one lookup.
func (s *Store) Delete(id int) error {
	res, err := s.db.Exec(`DELETE FROM repos WHERE id = $1`, id)
	if err != nil {
		return err
	}
	return checkAffected(res)
}

// Grant gives username access to repoID. Idempotent: granting something
// already granted is not an error.
func (s *Store) Grant(username string, repoID int) error {
	_, err := s.db.Exec(
		`INSERT INTO user_repo_access (username, repo_id) VALUES ($1, $2)
		 ON CONFLICT (username, repo_id) DO NOTHING`,
		username, repoID,
	)
	return err
}

// Revoke removes username's access to repoID. Not an error if they never
// had it, revoking is idempotent the same way granting is.
func (s *Store) Revoke(username string, repoID int) error {
	_, err := s.db.Exec(
		`DELETE FROM user_repo_access WHERE username = $1 AND repo_id = $2`,
		username, repoID,
	)
	return err
}

func checkAffected(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrRepoNotFound
	}
	return nil
}
