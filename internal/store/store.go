// Package store is this deployment's own database.
//
// AgentCell keeps its PLATFORM state in Kubernetes — Cells, Sessions and
// Teams are custom resources, because they describe workloads and the
// cluster is already the thing that reconciles them. People are not
// workloads. An account, an invitation, a lent credential and a linked
// forge identity are ordinary records that get read on every request,
// listed, joined and updated in place, and modelling them as Secrets meant
// a directory lookup was a label search and "who lent what to whom" had no
// place to live at all.
//
// So: SQLite, one file on a volume. It needs no server to run, no backup
// story beyond copying a file, and no second thing to upgrade. The driver
// is pure Go (modernc.org/sqlite) because celld builds with CGO disabled
// and ships on a distroless image — a cgo driver would have quietly forced
// both of those to change.
//
// One writer. celld runs a single replica for this deployment and SQLite
// is opened in WAL mode, so readers never block the writer; if this ever
// needs two replicas, that is the moment to move these tables to a real
// server, not to add locking here.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// ErrNotFound is returned instead of sql.ErrNoRows so callers do not have
// to import database/sql to tell "no such person" from "the database is
// broken" — a distinction that decides whether to answer 404 or 500.
var ErrNotFound = errors.New("not found")

type DB struct{ sql *sql.DB }

// Open prepares the database and applies the schema.
//
// The path is a file on a volume. Journal mode WAL is set before anything
// else because switching it later requires exclusive access, and busy_timeout
// so a concurrent write waits rather than failing the request outright.
func Open(path string) (*DB, error) {
	d, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	// SQLite takes one writer at a time. Letting the pool open several
	// connections does not make writes parallel; it makes them collide and
	// surface as "database is locked" under exactly the load where the
	// product needs to be dull.
	d.SetMaxOpenConns(1)
	db := &DB{sql: d}
	if err := db.migrate(); err != nil {
		_ = d.Close()
		return nil, err
	}
	return db, nil
}

func (db *DB) Close() error { return db.sql.Close() }

// migrate applies schema steps in order and records how far it got.
//
// Numbered steps rather than "CREATE TABLE IF NOT EXISTS" everywhere: the
// second kind silently does nothing when a table exists but is the wrong
// shape, which is how a deployment ends up running against a schema nobody
// can name.
func (db *DB) migrate() error {
	steps := []string{
		`CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS users (
			id          TEXT PRIMARY KEY,
			email       TEXT NOT NULL UNIQUE,
			name        TEXT NOT NULL DEFAULT '',
			password    TEXT NOT NULL DEFAULT '',
			is_admin    INTEGER NOT NULL DEFAULT 0,
			created_at  INTEGER NOT NULL,
			disabled_at INTEGER
		)`,
		`CREATE TABLE IF NOT EXISTS invites (
			token_hash TEXT PRIMARY KEY,
			email      TEXT NOT NULL,
			name       TEXT NOT NULL DEFAULT '',
			is_admin   INTEGER NOT NULL DEFAULT 0,
			invited_by TEXT NOT NULL DEFAULT '',
			expires_at INTEGER NOT NULL,
			created_at INTEGER NOT NULL
		)`,
		// A lent credential. grantee_kind is 'user' or 'team' so one table
		// answers both "lend it to Li" and "lend it to the platform team".
		`CREATE TABLE IF NOT EXISTS grants (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			granter_id   TEXT NOT NULL,
			grantee_kind TEXT NOT NULL,
			grantee_id   TEXT NOT NULL,
			provider     TEXT NOT NULL,
			created_at   INTEGER NOT NULL,
			UNIQUE (granter_id, grantee_kind, grantee_id, provider)
		)`,
		// A person's own forge identity. Never shared and never granted:
		// a commit pushed with somebody else's token is that person's
		// commit as far as GitLab is concerned, and an audit trail that
		// attributes work to the wrong human is worse than none.
		`CREATE TABLE IF NOT EXISTS git_identities (
			user_id    TEXT NOT NULL,
			provider   TEXT NOT NULL,
			username   TEXT NOT NULL DEFAULT '',
			token      TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			PRIMARY KEY (user_id, provider)
		)`,
	}
	for i, s := range steps {
		if _, err := db.sql.Exec(s); err != nil {
			return fmt.Errorf("schema step %d: %w", i, err)
		}
	}
	return nil
}

// --- users -----------------------------------------------------------

type User struct {
	ID       string
	Email    string
	Name     string
	Admin    bool
	Disabled bool
}

// NormalizeEmail folds the case-insensitive parts of an address, so
// "Zhu@Tinci.com" and "zhu@tinci.com" cannot become two people with two
// sets of credentials and two halves of the same person's work.
func NormalizeEmail(e string) string { return strings.ToLower(strings.TrimSpace(e)) }

func (db *DB) CreateUser(ctx context.Context, id, email, name, passwordHash string, admin bool) error {
	_, err := db.sql.ExecContext(ctx,
		`INSERT INTO users (id, email, name, password, is_admin, created_at) VALUES (?,?,?,?,?,?)`,
		id, NormalizeEmail(email), name, passwordHash, boolInt(admin), time.Now().Unix())
	return err
}

func (db *DB) UserByEmail(ctx context.Context, email string) (User, string, error) {
	row := db.sql.QueryRowContext(ctx,
		`SELECT id, email, name, password, is_admin, disabled_at IS NOT NULL FROM users WHERE email = ?`,
		NormalizeEmail(email))
	var u User
	var hash string
	if err := row.Scan(&u.ID, &u.Email, &u.Name, &hash, &u.Admin, &u.Disabled); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return User{}, "", ErrNotFound
		}
		return User{}, "", err
	}
	return u, hash, nil
}

func (db *DB) SetPassword(ctx context.Context, email, hash string) error {
	res, err := db.sql.ExecContext(ctx, `UPDATE users SET password = ? WHERE email = ?`,
		hash, NormalizeEmail(email))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (db *DB) Users(ctx context.Context) ([]User, error) {
	rows, err := db.sql.QueryContext(ctx,
		`SELECT id, email, name, is_admin, disabled_at IS NOT NULL FROM users ORDER BY email`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Email, &u.Name, &u.Admin, &u.Disabled); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (db *DB) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := db.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// SetDisabled turns an account off without deleting it. Deleting a person
// would orphan everything they own — their sessions, their worktrees, the
// authorship of what they built — so leaving is a state, not a removal.
func (db *DB) SetDisabled(ctx context.Context, email string, disabled bool) error {
	var v any
	if disabled {
		v = time.Now().Unix()
	}
	res, err := db.sql.ExecContext(ctx, `UPDATE users SET disabled_at = ? WHERE email = ?`,
		v, NormalizeEmail(email))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// --- invites ---------------------------------------------------------

type Invite struct {
	Email   string
	Name    string
	Admin   bool
	By      string
	Expires int64
}

func (db *DB) CreateInvite(ctx context.Context, tokenHash string, in Invite) error {
	_, err := db.sql.ExecContext(ctx,
		`INSERT INTO invites (token_hash, email, name, is_admin, invited_by, expires_at, created_at)
		 VALUES (?,?,?,?,?,?,?)`,
		tokenHash, NormalizeEmail(in.Email), in.Name, boolInt(in.Admin), in.By,
		in.Expires, time.Now().Unix())
	return err
}

// Invite looks up an unexpired invitation.
func (db *DB) Invite(ctx context.Context, tokenHash string) (Invite, error) {
	row := db.sql.QueryRowContext(ctx,
		`SELECT email, name, is_admin, invited_by, expires_at FROM invites WHERE token_hash = ?`,
		tokenHash)
	var in Invite
	if err := row.Scan(&in.Email, &in.Name, &in.Admin, &in.By, &in.Expires); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Invite{}, ErrNotFound
		}
		return Invite{}, err
	}
	if time.Now().Unix() > in.Expires {
		return Invite{}, ErrNotFound
	}
	return in, nil
}

func (db *DB) DeleteInvite(ctx context.Context, tokenHash string) error {
	_, err := db.sql.ExecContext(ctx, `DELETE FROM invites WHERE token_hash = ?`, tokenHash)
	return err
}

// PendingInvites lists invitations that have not been redeemed, so an
// admin can see who has been asked and re-send rather than inviting the
// same person twice.
func (db *DB) PendingInvites(ctx context.Context) ([]Invite, error) {
	rows, err := db.sql.QueryContext(ctx,
		`SELECT email, name, is_admin, invited_by, expires_at FROM invites
		 WHERE expires_at > ? ORDER BY created_at DESC`, time.Now().Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Invite
	for rows.Next() {
		var in Invite
		if err := rows.Scan(&in.Email, &in.Name, &in.Admin, &in.By, &in.Expires); err != nil {
			return nil, err
		}
		out = append(out, in)
	}
	return out, rows.Err()
}

// --- grants ----------------------------------------------------------

const (
	GranteeUser = "user"
	GranteeTeam = "team"
)

type Grant struct {
	GranterID   string
	GranteeKind string
	GranteeID   string
	Provider    string
}

func (db *DB) CreateGrant(ctx context.Context, g Grant) error {
	_, err := db.sql.ExecContext(ctx,
		`INSERT OR IGNORE INTO grants (granter_id, grantee_kind, grantee_id, provider, created_at)
		 VALUES (?,?,?,?,?)`,
		g.GranterID, g.GranteeKind, g.GranteeID, g.Provider, time.Now().Unix())
	return err
}

func (db *DB) DeleteGrant(ctx context.Context, g Grant) error {
	_, err := db.sql.ExecContext(ctx,
		`DELETE FROM grants WHERE granter_id=? AND grantee_kind=? AND grantee_id=? AND provider=?`,
		g.GranterID, g.GranteeKind, g.GranteeID, g.Provider)
	return err
}

// GrantsTo returns the credentials lent to this person — directly, or
// through a team they belong to. Teams live in Kubernetes, so the caller
// passes the team ids it already resolved rather than this package growing
// a second idea of what a team is.
func (db *DB) GrantsTo(ctx context.Context, userID string, teamIDs []string) ([]Grant, error) {
	q := `SELECT granter_id, grantee_kind, grantee_id, provider FROM grants
	      WHERE (grantee_kind='user' AND grantee_id=?)`
	args := []any{userID}
	if len(teamIDs) > 0 {
		q += ` OR (grantee_kind='team' AND grantee_id IN (` +
			strings.TrimSuffix(strings.Repeat("?,", len(teamIDs)), ",") + `))`
		for _, t := range teamIDs {
			args = append(args, t)
		}
	}
	rows, err := db.sql.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Grant
	for rows.Next() {
		var g Grant
		if err := rows.Scan(&g.GranterID, &g.GranteeKind, &g.GranteeID, &g.Provider); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// GrantsBy returns what this person has lent out, so they can see it and
// take it back.
func (db *DB) GrantsBy(ctx context.Context, granterID string) ([]Grant, error) {
	rows, err := db.sql.QueryContext(ctx,
		`SELECT granter_id, grantee_kind, grantee_id, provider FROM grants
		 WHERE granter_id = ? ORDER BY created_at DESC`, granterID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Grant
	for rows.Next() {
		var g Grant
		if err := rows.Scan(&g.GranterID, &g.GranteeKind, &g.GranteeID, &g.Provider); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// --- git identities --------------------------------------------------

type GitIdentity struct {
	Provider string
	Username string
	Token    string
}

func (db *DB) SetGitIdentity(ctx context.Context, userID string, g GitIdentity) error {
	_, err := db.sql.ExecContext(ctx,
		`INSERT INTO git_identities (user_id, provider, username, token, created_at)
		 VALUES (?,?,?,?,?)
		 ON CONFLICT(user_id, provider) DO UPDATE SET username=excluded.username, token=excluded.token`,
		userID, g.Provider, g.Username, g.Token, time.Now().Unix())
	return err
}

func (db *DB) GitIdentity(ctx context.Context, userID, provider string) (GitIdentity, error) {
	row := db.sql.QueryRowContext(ctx,
		`SELECT provider, username, token FROM git_identities WHERE user_id=? AND provider=?`,
		userID, provider)
	var g GitIdentity
	if err := row.Scan(&g.Provider, &g.Username, &g.Token); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return GitIdentity{}, ErrNotFound
		}
		return GitIdentity{}, err
	}
	return g, nil
}

// GitProviders reports which forges this person has linked, without
// returning the tokens — the console needs to show a tick, not a secret.
func (db *DB) GitProviders(ctx context.Context, userID string) (map[string]string, error) {
	rows, err := db.sql.QueryContext(ctx,
		`SELECT provider, username FROM git_identities WHERE user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var p, u string
		if err := rows.Scan(&p, &u); err != nil {
			return nil, err
		}
		out[p] = u
	}
	return out, rows.Err()
}

func (db *DB) DeleteGitIdentity(ctx context.Context, userID, provider string) error {
	_, err := db.sql.ExecContext(ctx,
		`DELETE FROM git_identities WHERE user_id=? AND provider=?`, userID, provider)
	return err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
