package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound = errors.New("not found")
	ErrInvalid  = errors.New("invalid input")
)

// InvalidError wraps ErrInvalid with a user-facing message.
type InvalidError struct{ Msg string }

func (e InvalidError) Error() string { return e.Msg }
func (e InvalidError) Unwrap() error { return ErrInvalid }

func invalid(msg string) error { return InvalidError{Msg: msg} }

type Store struct {
	db     *pgxpool.Pool
	health *health
	// DefaultRole is a non-admin's role in a workspace they have no explicit
	// role in (config DEFAULT_WORKSPACE_ROLE); empty means RoleViewer.
	DefaultRole string
}

// defaultRole is DefaultRole, or RoleViewer when unset.
func (s *Store) defaultRole() string {
	if s.DefaultRole == "" {
		return RoleViewer
	}
	return s.DefaultRole
}

// memberOf is the SQL condition "user <u>.id can see a workspace in the
// text[] parameter <scope>", using the text parameter <def> as the default
// role (see migration 010). A NULL scope (every workspace) matches everyone.
func memberOf(u, scope, def string) string {
	return `(` + scope + `::text[] IS NULL OR EXISTS (
		SELECT 1 FROM workspaces mw
		LEFT JOIN workspace_members mm ON mm.workspace_id = mw.id AND mm.user_id = ` + u + `.id
		WHERE mw.id::text = ANY(` + scope + `) AND coalesce(mm.role, ` + def + `::text) IN ('editor', 'viewer')))`
}

func New(db *pgxpool.Pool) *Store { return &Store{db: db, health: newHealth()} }

// Ping checks that the database answers (for /readyz).
func (s *Store) Ping(ctx context.Context) error { return s.db.Ping(ctx) }
