// Package users backs the login allowlist with Postgres instead of a
// hardcoded slice in main.go. Schema setup and the initial admin bootstrap
// are raw-SQL operations run from CI/CD (see cmd/api/README.md) — this
// package never migrates or seeds anything itself, and checks the schema
// exists at startup rather than creating it. Ongoing user management (list,
// grant, change role, revoke) has two paths: the raw-SQL commands in the
// README, or the CRUD methods below, used by the /api/v1/admin/users handlers.
package users

import (
	"database/sql"
	"errors"
	"time"

	"github.com/lib/pq"
)

// Role is a closed set — enforced in Postgres too, via a CHECK constraint
// (see migrations/0002_add_role_to_allowed_users.sql).
type Role string

const (
	RoleAdmin     Role = "admin"
	RoleDeveloper Role = "developer"
)

func (r Role) valid() bool {
	return r == RoleAdmin || r == RoleDeveloper
}

var (
	ErrUserExists   = errors.New("user already exists")
	ErrUserNotFound = errors.New("user not found")
	ErrInvalidRole  = errors.New("role must be \"admin\" or \"developer\"")
	ErrLastAdmin    = errors.New("cannot remove or demote the only remaining admin")
)

type User struct {
	Username string    `json:"username"`
	Role     Role      `json:"role"`
	AddedAt  time.Time `json:"addedAt"`
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

// CheckSchema fails fast if allowed_users hasn't been migrated yet, instead
// of the service silently coming up against a database that isn't ready.
func (s *Store) CheckSchema() error {
	var dummy int
	err := s.db.QueryRow(`SELECT 1 FROM allowed_users LIMIT 1`).Scan(&dummy)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	return nil
}

// Lookup reports whether username is allowed to log in and, if so, their
// role. Fails closed: a username with no row means allowed=false — an
// unseeded allowlist should never mean open access. role is meaningless
// when allowed is false. See cmd/api/migrations/seed/seed-admin.sql for
// granting the first (admin) user.
func (s *Store) Lookup(username string) (allowed bool, role Role, err error) {
	var r string
	err = s.db.QueryRow(`SELECT role FROM allowed_users WHERE username = $1`, username).Scan(&r)
	if err == sql.ErrNoRows {
		return false, "", nil
	}
	if err != nil {
		return false, "", err
	}
	return true, Role(r), nil
}

// List returns every allowed user, ordered by username.
func (s *Store) List() ([]User, error) {
	rows, err := s.db.Query(`SELECT username, role, added_at FROM allowed_users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []User
	for rows.Next() {
		var u User
		var role string
		if err := rows.Scan(&u.Username, &role, &u.AddedAt); err != nil {
			return nil, err
		}
		u.Role = Role(role)
		list = append(list, u)
	}
	return list, rows.Err()
}

// Add grants username access with the given role. ErrUserExists if they're
// already granted, ErrInvalidRole if role isn't admin/developer.
func (s *Store) Add(username string, role Role) error {
	if !role.valid() {
		return ErrInvalidRole
	}
	_, err := s.db.Exec(`INSERT INTO allowed_users (username, role) VALUES ($1, $2)`, username, role)
	var pqErr *pq.Error
	if errors.As(err, &pqErr) && pqErr.Code == "23505" { // unique_violation
		return ErrUserExists
	}
	return err
}

// SetRole changes an existing user's role. ErrUserNotFound if they aren't
// granted, ErrInvalidRole if role isn't admin/developer, ErrLastAdmin if
// this would demote the only remaining admin.
func (s *Store) SetRole(username string, role Role) error {
	if !role.valid() {
		return ErrInvalidRole
	}
	if role != RoleAdmin {
		isLast, err := s.isLastAdmin(username)
		if err != nil {
			return err
		}
		if isLast {
			return ErrLastAdmin
		}
	}
	res, err := s.db.Exec(`UPDATE allowed_users SET role = $1 WHERE username = $2`, role, username)
	if err != nil {
		return err
	}
	return checkAffected(res)
}

// Remove revokes username's access entirely. ErrUserNotFound if they aren't
// granted, ErrLastAdmin if this would remove the only remaining admin.
func (s *Store) Remove(username string) error {
	isLast, err := s.isLastAdmin(username)
	if err != nil {
		return err
	}
	if isLast {
		return ErrLastAdmin
	}
	res, err := s.db.Exec(`DELETE FROM allowed_users WHERE username = $1`, username)
	if err != nil {
		return err
	}
	return checkAffected(res)
}

// isLastAdmin reports whether username is currently the only admin — i.e.
// whether removing or demoting them would leave zero admins able to manage
// access at all.
func (s *Store) isLastAdmin(username string) (bool, error) {
	var role string
	err := s.db.QueryRow(`SELECT role FROM allowed_users WHERE username = $1`, username).Scan(&role)
	if err == sql.ErrNoRows {
		return false, ErrUserNotFound
	}
	if err != nil {
		return false, err
	}
	if role != string(RoleAdmin) {
		return false, nil
	}

	var adminCount int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM allowed_users WHERE role = $1`, RoleAdmin).Scan(&adminCount); err != nil {
		return false, err
	}
	return adminCount <= 1, nil
}

func checkAffected(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrUserNotFound
	}
	return nil
}
