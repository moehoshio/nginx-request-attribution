// Package auth implements user accounts, session-based login, and
// CSRF protection. See docs/ROADMAP.md (Phase 2) for the design.
//
// The service owns three tables: users, sessions, audit_log. It speaks
// only to *sql.DB; it does not depend on the storage package because the
// auth schema is intentionally independent of the request-log schema.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// Roles. Kept as plain strings because the set is tiny and stable.
const (
	RoleAdmin  = "admin"
	RoleViewer = "viewer"
)

// DefaultSessionTTL is how long a freshly minted session lives.
const DefaultSessionTTL = 24 * time.Hour

// MinPasswordLength is enforced on create/change. 8 is a common floor;
// bcrypt's own max-72-byte limit is handled in Authenticate.
const MinPasswordLength = 8

// Sentinel errors callers can match with errors.Is.
var (
	ErrInvalidCredentials = errors.New("auth: invalid username or password")
	ErrUserDisabled       = errors.New("auth: user is disabled")
	ErrUserNotFound       = errors.New("auth: user not found")
	ErrUserExists         = errors.New("auth: username already exists")
	ErrWeakPassword       = errors.New("auth: password too short")
	ErrInvalidRole        = errors.New("auth: invalid role")
	ErrSessionExpired     = errors.New("auth: session expired")
	ErrSessionNotFound    = errors.New("auth: session not found")
)

// User is the public view of an account row. password_hash never leaves
// the package.
type User struct {
	ID          int64      `json:"id"`
	Username    string     `json:"username"`
	Role        string     `json:"role"`
	Disabled    bool       `json:"disabled"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	LastLoginAt *time.Time `json:"last_login_at,omitempty"`
}

// Session is the row stored in the sessions table. The plaintext token
// is returned only from CreateSession and never persisted.
type Session struct {
	ID        int64
	UserID    int64
	ExpiresAt time.Time
	CreatedAt time.Time
}

// Service bundles the auth-related operations together.
type Service struct {
	db           *sql.DB
	bcryptCost   int
	sessionTTL   time.Duration
	cookieSecure bool
	now          func() time.Time // injectable for tests
}

// Options is the constructor argument. Zero values get sensible defaults.
type Options struct {
	// BcryptCost is the bcrypt cost parameter. 0 → bcrypt.DefaultCost (10);
	// production deployments should pick 12+.
	BcryptCost int
	// SessionTTL overrides DefaultSessionTTL when non-zero.
	SessionTTL time.Duration
	// CookieSecure makes auth cookies require HTTPS. Recommended in
	// production; left off by default so local `http://` development
	// works without surprises.
	CookieSecure bool
}

// New constructs a Service and runs migrations.
func New(db *sql.DB, opts Options) (*Service, error) {
	cost := opts.BcryptCost
	if cost == 0 {
		cost = bcrypt.DefaultCost
	}
	if cost < bcrypt.MinCost || cost > bcrypt.MaxCost {
		return nil, fmt.Errorf("auth: bcrypt cost %d out of range [%d,%d]", cost, bcrypt.MinCost, bcrypt.MaxCost)
	}
	ttl := opts.SessionTTL
	if ttl == 0 {
		ttl = DefaultSessionTTL
	}
	s := &Service{
		db:           db,
		bcryptCost:   cost,
		sessionTTL:   ttl,
		cookieSecure: opts.CookieSecure,
		now:          time.Now,
	}
	if err := s.migrate(); err != nil {
		return nil, fmt.Errorf("auth: migrate: %w", err)
	}
	return s, nil
}

// CookieSecure reports whether cookies should be issued with the
// Secure attribute. Exported so the handler layer can build cookies.
func (s *Service) CookieSecure() bool { return s.cookieSecure }

// SessionTTL exposes the configured TTL.
func (s *Service) SessionTTL() time.Duration { return s.sessionTTL }

func (s *Service) migrate() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			username TEXT NOT NULL UNIQUE COLLATE NOCASE,
			password_hash TEXT NOT NULL,
			role TEXT NOT NULL,
			disabled INTEGER NOT NULL DEFAULT 0,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			last_login_at DATETIME
		);

		CREATE TABLE IF NOT EXISTS sessions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL,
			token_hash TEXT NOT NULL UNIQUE,
			created_at DATETIME NOT NULL,
			expires_at DATETIME NOT NULL,
			ip TEXT,
			user_agent TEXT,
			FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
		);
		CREATE INDEX IF NOT EXISTS idx_sessions_user_id ON sessions(user_id);
		CREATE INDEX IF NOT EXISTS idx_sessions_expires_at ON sessions(expires_at);

		CREATE TABLE IF NOT EXISTS audit_log (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER,
			action TEXT NOT NULL,
			target TEXT,
			ip TEXT,
			created_at DATETIME NOT NULL,
			detail TEXT
		);
		CREATE INDEX IF NOT EXISTS idx_audit_log_created_at ON audit_log(created_at);
	`)
	return err
}

// validRole returns true if r is one of the known roles.
func validRole(r string) bool {
	return r == RoleAdmin || r == RoleViewer
}

// normalizeUsername lowercases and trims whitespace. Stored column uses
// NOCASE collation, but we still normalise on input for consistency in
// audit logs and downstream comparisons.
func normalizeUsername(u string) string {
	return strings.ToLower(strings.TrimSpace(u))
}

// CountUsers returns the number of rows in the users table. Used by the
// bootstrap-admin flow.
func (s *Service) CountUsers() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// CreateUser inserts a new account. The plaintext password is hashed
// with bcrypt; only the hash is stored.
func (s *Service) CreateUser(username, password, role string) (*User, error) {
	username = normalizeUsername(username)
	if username == "" {
		return nil, fmt.Errorf("auth: username required")
	}
	if !validRole(role) {
		return nil, ErrInvalidRole
	}
	if len(password) < MinPasswordLength {
		return nil, ErrWeakPassword
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), s.bcryptCost)
	if err != nil {
		return nil, fmt.Errorf("auth: hash password: %w", err)
	}
	now := s.now().UTC()
	res, err := s.db.Exec(
		`INSERT INTO users (username, password_hash, role, disabled, created_at, updated_at)
		 VALUES (?, ?, ?, 0, ?, ?)`,
		username, string(hash), role, now, now,
	)
	if err != nil {
		// SQLite reports "UNIQUE constraint failed" on duplicates;
		// translate to a typed error so callers can react.
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, ErrUserExists
		}
		return nil, err
	}
	id, _ := res.LastInsertId()
	return &User{
		ID: id, Username: username, Role: role,
		CreatedAt: now, UpdatedAt: now,
	}, nil
}

// BootstrapAdmin creates the admin user described by (username,
// password) iff the users table is empty. It returns (created, err)
// where created is true only on the initial creation.
func (s *Service) BootstrapAdmin(username, password string) (bool, error) {
	n, err := s.CountUsers()
	if err != nil {
		return false, err
	}
	if n > 0 {
		return false, nil
	}
	if _, err := s.CreateUser(username, password, RoleAdmin); err != nil {
		return false, err
	}
	return true, nil
}

// GetUser fetches by id.
func (s *Service) GetUser(id int64) (*User, error) {
	u := &User{}
	var last sql.NullTime
	var disabled int
	err := s.db.QueryRow(
		`SELECT id, username, role, disabled, created_at, updated_at, last_login_at
		 FROM users WHERE id = ?`, id,
	).Scan(&u.ID, &u.Username, &u.Role, &disabled, &u.CreatedAt, &u.UpdatedAt, &last)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUserNotFound
	}
	if err != nil {
		return nil, err
	}
	u.Disabled = disabled != 0
	if last.Valid {
		t := last.Time
		u.LastLoginAt = &t
	}
	return u, nil
}

// ListUsers returns all users ordered by id.
func (s *Service) ListUsers() ([]*User, error) {
	rows, err := s.db.Query(
		`SELECT id, username, role, disabled, created_at, updated_at, last_login_at
		 FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*User
	for rows.Next() {
		u := &User{}
		var last sql.NullTime
		var disabled int
		if err := rows.Scan(&u.ID, &u.Username, &u.Role, &disabled, &u.CreatedAt, &u.UpdatedAt, &last); err != nil {
			return nil, err
		}
		u.Disabled = disabled != 0
		if last.Valid {
			t := last.Time
			u.LastLoginAt = &t
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// UpdateUser changes role and/or disabled. Nil fields are left alone.
// Username is intentionally immutable in Phase 2.
func (s *Service) UpdateUser(id int64, role *string, disabled *bool) (*User, error) {
	if role == nil && disabled == nil {
		return s.GetUser(id)
	}
	sets := []string{"updated_at = ?"}
	args := []interface{}{s.now().UTC()}
	if role != nil {
		if !validRole(*role) {
			return nil, ErrInvalidRole
		}
		sets = append(sets, "role = ?")
		args = append(args, *role)
	}
	if disabled != nil {
		sets = append(sets, "disabled = ?")
		d := 0
		if *disabled {
			d = 1
		}
		args = append(args, d)
	}
	args = append(args, id)
	res, err := s.db.Exec(
		`UPDATE users SET `+strings.Join(sets, ", ")+` WHERE id = ?`,
		args...,
	)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, ErrUserNotFound
	}
	// If disabling the user, revoke their active sessions.
	if disabled != nil && *disabled {
		if _, err := s.db.Exec(`DELETE FROM sessions WHERE user_id = ?`, id); err != nil {
			return nil, err
		}
	}
	return s.GetUser(id)
}

// DeleteUser removes the user and (via ON DELETE CASCADE) their sessions.
func (s *Service) DeleteUser(id int64) error {
	res, err := s.db.Exec(`DELETE FROM users WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrUserNotFound
	}
	return nil
}

// ChangePassword updates a user's password. The caller is expected to
// have already verified the user's identity (e.g. via existing session
// or by being an admin).
func (s *Service) ChangePassword(id int64, newPassword string) error {
	if len(newPassword) < MinPasswordLength {
		return ErrWeakPassword
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), s.bcryptCost)
	if err != nil {
		return err
	}
	res, err := s.db.Exec(
		`UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?`,
		string(hash), s.now().UTC(), id,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrUserNotFound
	}
	// Invalidate existing sessions on password change.
	_, err = s.db.Exec(`DELETE FROM sessions WHERE user_id = ?`, id)
	return err
}

// Authenticate verifies username + password and returns the user. It
// performs a dummy bcrypt comparison even on unknown users so an
// attacker can't distinguish "user doesn't exist" from "wrong
// password" by timing.
func (s *Service) Authenticate(username, password string) (*User, error) {
	// bcrypt rejects inputs > 72 bytes outright; truncate so the
	// dummy comparison below still runs the same code path.
	if len(password) > 72 {
		password = password[:72]
	}
	username = normalizeUsername(username)
	var (
		id        int64
		hash      string
		role      string
		disabled  int
		createdAt time.Time
		updatedAt time.Time
		last      sql.NullTime
	)
	err := s.db.QueryRow(
		`SELECT id, password_hash, role, disabled, created_at, updated_at, last_login_at
		 FROM users WHERE username = ?`, username,
	).Scan(&id, &hash, &role, &disabled, &createdAt, &updatedAt, &last)
	if errors.Is(err, sql.ErrNoRows) {
		// Constant-time-ish: still run bcrypt against a known hash so
		// the response time doesn't reveal user existence.
		_ = bcrypt.CompareHashAndPassword([]byte(dummyBcryptHash), []byte(password))
		return nil, ErrInvalidCredentials
	}
	if err != nil {
		return nil, err
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); err != nil {
		return nil, ErrInvalidCredentials
	}
	if disabled != 0 {
		return nil, ErrUserDisabled
	}
	u := &User{
		ID: id, Username: username, Role: role,
		CreatedAt: createdAt, UpdatedAt: updatedAt,
	}
	if last.Valid {
		t := last.Time
		u.LastLoginAt = &t
	}
	return u, nil
}

// dummyBcryptHash is a precomputed bcrypt hash used only to keep the
// "user not found" branch in Authenticate roughly the same cost as the
// "user found" branch. The plaintext is unimportant; it never matches
// a real user's password.
const dummyBcryptHash = "$2a$10$CwTycUXWue0Thq9StjUM0uJ8fIuCa/4qB7psbeu62.AeC4N5Ggp.S"

// CreateSession mints a fresh session for u. Returns the plaintext
// token (to be set in a cookie) and the persisted row.
func (s *Service) CreateSession(userID int64, ip, userAgent string) (token string, sess *Session, err error) {
	token, err = generateToken()
	if err != nil {
		return "", nil, err
	}
	now := s.now().UTC()
	expires := now.Add(s.sessionTTL)
	res, err := s.db.Exec(
		`INSERT INTO sessions (user_id, token_hash, created_at, expires_at, ip, user_agent)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		userID, hashToken(token), now, expires, ip, userAgent,
	)
	if err != nil {
		return "", nil, err
	}
	id, _ := res.LastInsertId()
	_, _ = s.db.Exec(
		`UPDATE users SET last_login_at = ? WHERE id = ?`, now, userID,
	)
	return token, &Session{
		ID: id, UserID: userID, ExpiresAt: expires, CreatedAt: now,
	}, nil
}

// ValidateSession looks up the session by its plaintext token, returns
// the owning user, and prunes the row if it is expired.
func (s *Service) ValidateSession(token string) (*User, *Session, error) {
	if token == "" {
		return nil, nil, ErrSessionNotFound
	}
	th := hashToken(token)
	var sess Session
	var userID int64
	err := s.db.QueryRow(
		`SELECT id, user_id, created_at, expires_at FROM sessions WHERE token_hash = ?`, th,
	).Scan(&sess.ID, &userID, &sess.CreatedAt, &sess.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, ErrSessionNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	sess.UserID = userID
	if !s.now().UTC().Before(sess.ExpiresAt) {
		_, _ = s.db.Exec(`DELETE FROM sessions WHERE id = ?`, sess.ID)
		return nil, nil, ErrSessionExpired
	}
	u, err := s.GetUser(userID)
	if err != nil {
		return nil, nil, err
	}
	if u.Disabled {
		_, _ = s.db.Exec(`DELETE FROM sessions WHERE id = ?`, sess.ID)
		return nil, nil, ErrUserDisabled
	}
	return u, &sess, nil
}

// DeleteSession removes the session matching token. Missing rows are
// silently ignored (idempotent logout).
func (s *Service) DeleteSession(token string) error {
	if token == "" {
		return nil
	}
	_, err := s.db.Exec(`DELETE FROM sessions WHERE token_hash = ?`, hashToken(token))
	return err
}

// PurgeExpiredSessions deletes all sessions whose expiry has passed.
// Safe to call periodically from a background goroutine.
func (s *Service) PurgeExpiredSessions() (int64, error) {
	res, err := s.db.Exec(`DELETE FROM sessions WHERE expires_at < ?`, s.now().UTC())
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// Audit appends a row to audit_log. Errors are returned but most
// callers log-and-continue: an audit failure shouldn't block a
// user-visible action.
func (s *Service) Audit(userID *int64, action, target, ip, detail string) error {
	var uid interface{}
	if userID != nil {
		uid = *userID
	}
	_, err := s.db.Exec(
		`INSERT INTO audit_log (user_id, action, target, ip, created_at, detail)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		uid, action, target, ip, s.now().UTC(), detail,
	)
	return err
}

// generateToken returns a 256-bit URL-safe random string.
func generateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// hashToken returns a hex sha256 of the token. We hash on the way in
// so a DB read leak doesn't immediately yield usable session cookies.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// constantTimeEqual compares two strings without leaking length info
// beyond what subtle.ConstantTimeCompare exposes. Returns false when
// lengths differ.
func constantTimeEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
