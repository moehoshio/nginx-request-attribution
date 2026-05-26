package auth

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"golang.org/x/crypto/bcrypt"
)

// newTestDB returns an in-memory SQLite database.
func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:?_foreign_keys=on")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// newTestService builds a Service with the cheapest bcrypt cost so the
// tests stay fast.
func newTestService(t *testing.T) *Service {
	t.Helper()
	db := newTestDB(t)
	svc, err := New(db, Options{BcryptCost: bcrypt.MinCost})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return svc
}

func TestCreateUserAndAuthenticate(t *testing.T) {
	svc := newTestService(t)

	u, err := svc.CreateUser("Alice", "correct horse battery staple", RoleAdmin)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if u.Username != "alice" {
		t.Errorf("username not normalised: %q", u.Username)
	}
	if u.Role != RoleAdmin {
		t.Errorf("role = %q, want admin", u.Role)
	}

	got, err := svc.Authenticate("ALICE", "correct horse battery staple")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if got.ID != u.ID {
		t.Errorf("ID mismatch: got %d want %d", got.ID, u.ID)
	}

	if _, err := svc.Authenticate("alice", "wrong"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("wrong password: got %v want ErrInvalidCredentials", err)
	}
	if _, err := svc.Authenticate("nobody", "whatever"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("unknown user: got %v want ErrInvalidCredentials", err)
	}
}

func TestCreateUserValidation(t *testing.T) {
	svc := newTestService(t)

	if _, err := svc.CreateUser("bob", "short", RoleViewer); !errors.Is(err, ErrWeakPassword) {
		t.Errorf("short password: got %v want ErrWeakPassword", err)
	}
	if _, err := svc.CreateUser("bob", "longenough", "wizard"); !errors.Is(err, ErrInvalidRole) {
		t.Errorf("bad role: got %v want ErrInvalidRole", err)
	}
	if _, err := svc.CreateUser("bob", "longenough", RoleViewer); err != nil {
		t.Fatalf("first create: %v", err)
	}
	if _, err := svc.CreateUser("BOB", "longenough2", RoleViewer); !errors.Is(err, ErrUserExists) {
		t.Errorf("duplicate (case-insensitive): got %v want ErrUserExists", err)
	}
}

func TestBootstrapAdminOnlyOnce(t *testing.T) {
	svc := newTestService(t)
	created, err := svc.BootstrapAdmin("root", "rootroot1")
	if err != nil || !created {
		t.Fatalf("first bootstrap: created=%v err=%v", created, err)
	}
	created, err = svc.BootstrapAdmin("root2", "rootroot2")
	if err != nil {
		t.Fatalf("second bootstrap: %v", err)
	}
	if created {
		t.Errorf("second bootstrap should not have created a user")
	}
	users, _ := svc.ListUsers()
	if len(users) != 1 {
		t.Errorf("user count = %d, want 1", len(users))
	}
}

func TestSessionLifecycle(t *testing.T) {
	svc := newTestService(t)
	u, err := svc.CreateUser("eve", "passpasspass", RoleViewer)
	if err != nil {
		t.Fatal(err)
	}

	token, sess, err := svc.CreateSession(u.ID, "1.2.3.4", "ua-test")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if token == "" || sess.ID == 0 {
		t.Fatalf("unexpected zero values: token=%q sess=%+v", token, sess)
	}

	got, _, err := svc.ValidateSession(token)
	if err != nil {
		t.Fatalf("ValidateSession: %v", err)
	}
	if got.ID != u.ID {
		t.Errorf("validated user ID = %d, want %d", got.ID, u.ID)
	}

	// Bad token.
	if _, _, err := svc.ValidateSession("not-a-token"); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("bad token: got %v want ErrSessionNotFound", err)
	}

	// Expired session.
	svc.now = func() time.Time { return sess.ExpiresAt.Add(time.Hour) }
	if _, _, err := svc.ValidateSession(token); !errors.Is(err, ErrSessionExpired) {
		t.Errorf("expired: got %v want ErrSessionExpired", err)
	}
	svc.now = time.Now

	// Logout / delete.
	token2, _, err := svc.CreateSession(u.ID, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteSession(token2); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.ValidateSession(token2); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("after delete: got %v want ErrSessionNotFound", err)
	}
}

func TestDisableUserRevokesSessions(t *testing.T) {
	svc := newTestService(t)
	u, _ := svc.CreateUser("dora", "passpasspass", RoleViewer)
	token, _, _ := svc.CreateSession(u.ID, "", "")

	disabled := true
	if _, err := svc.UpdateUser(u.ID, nil, &disabled); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.ValidateSession(token); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("disabled: got %v want ErrSessionNotFound", err)
	}
}

func TestChangePasswordInvalidatesSessions(t *testing.T) {
	svc := newTestService(t)
	u, _ := svc.CreateUser("carol", "passpasspass", RoleViewer)
	token, _, _ := svc.CreateSession(u.ID, "", "")

	if err := svc.ChangePassword(u.ID, "newpassword"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.ValidateSession(token); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("after password change: got %v want ErrSessionNotFound", err)
	}
	if _, err := svc.Authenticate("carol", "passpasspass"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("old password: got %v want ErrInvalidCredentials", err)
	}
	if _, err := svc.Authenticate("carol", "newpassword"); err != nil {
		t.Errorf("new password: %v", err)
	}
}

func TestPurgeExpiredSessions(t *testing.T) {
	svc := newTestService(t)
	u, _ := svc.CreateUser("frank", "passpasspass", RoleViewer)

	// Two live, one expired.
	_, _, _ = svc.CreateSession(u.ID, "", "")
	_, _, _ = svc.CreateSession(u.ID, "", "")
	svc.now = func() time.Time { return time.Now().Add(-48 * time.Hour) }
	_, _, _ = svc.CreateSession(u.ID, "", "")
	svc.now = time.Now

	n, err := svc.PurgeExpiredSessions()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("purged %d, want 1", n)
	}
}

// ---- handler / middleware tests ------------------------------------

func loginCookies(t *testing.T, h *Handler, username, password string) []*http.Cookie {
	t.Helper()
	body := strings.NewReader(`{"username":"` + username + `","password":"` + password + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", body)
	rr := httptest.NewRecorder()
	h.handleLogin(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("login: status %d, body=%s", rr.Code, rr.Body.String())
	}
	return rr.Result().Cookies()
}

func findCookie(cookies []*http.Cookie, name string) *http.Cookie {
	for _, c := range cookies {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func TestLoginIssuesCookies(t *testing.T) {
	svc := newTestService(t)
	h := NewHandler(svc)
	if _, err := svc.CreateUser("alice", "passpasspass", RoleAdmin); err != nil {
		t.Fatal(err)
	}

	cookies := loginCookies(t, h, "alice", "passpasspass")
	sess := findCookie(cookies, SessionCookieName)
	if sess == nil || sess.Value == "" {
		t.Fatalf("missing session cookie")
	}
	if !sess.HttpOnly {
		t.Errorf("session cookie should be HttpOnly")
	}
	csrf := findCookie(cookies, CSRFCookieName)
	if csrf == nil || csrf.Value == "" {
		t.Fatalf("missing csrf cookie")
	}
	if csrf.HttpOnly {
		t.Errorf("csrf cookie must not be HttpOnly (JS needs to read it)")
	}
}

func TestRequireAuthBlocksAnonymous(t *testing.T) {
	svc := newTestService(t)
	// Create a user so we exit "no-account mode"; RequireAuth must
	// block unauthenticated callers once at least one account exists.
	if _, err := svc.CreateUser("admin", "passpasspass", RoleAdmin); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(svc)
	called := false
	protected := h.RequireAuth(func(w http.ResponseWriter, r *http.Request) { called = true })

	req := httptest.NewRequest(http.MethodGet, "/api/stats", nil)
	rr := httptest.NewRecorder()
	protected(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
	if called {
		t.Errorf("handler should not be called for anonymous request")
	}
}

// TestRequireAuthAllowsAnonymousInNoAccountMode covers the bootstrap
// state: with zero users in the DB, RequireAuth (and RequireAdmin) let
// every request through so the operator can configure the instance
// from the UI before creating an account.
func TestRequireAuthAllowsAnonymousInNoAccountMode(t *testing.T) {
	svc := newTestService(t)
	h := NewHandler(svc)
	called := false
	protected := h.RequireAuth(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodGet, "/api/stats", nil)
	rr := httptest.NewRecorder()
	protected(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("no-account-mode RequireAuth status = %d, want 200", rr.Code)
	}
	if !called {
		t.Errorf("inner handler should run in no-account mode")
	}

	adminCalled := false
	admin := h.RequireAdmin(func(w http.ResponseWriter, r *http.Request) {
		adminCalled = true
		w.WriteHeader(http.StatusOK)
	})
	rr2 := httptest.NewRecorder()
	admin(rr2, httptest.NewRequest(http.MethodGet, "/api/users", nil))
	if rr2.Code != http.StatusOK {
		t.Errorf("no-account-mode RequireAdmin status = %d, want 200", rr2.Code)
	}
	if !adminCalled {
		t.Errorf("inner admin handler should run in no-account mode")
	}

	// Once a user exists, anonymous access is blocked again.
	if _, err := svc.CreateUser("admin", "passpasspass", RoleAdmin); err != nil {
		t.Fatal(err)
	}
	rr3 := httptest.NewRecorder()
	protected(rr3, httptest.NewRequest(http.MethodGet, "/api/stats", nil))
	if rr3.Code != http.StatusUnauthorized {
		t.Errorf("after first user, status = %d, want 401", rr3.Code)
	}
}

func TestRequireAuthAllowsValidSession(t *testing.T) {
	svc := newTestService(t)
	h := NewHandler(svc)
	if _, err := svc.CreateUser("alice", "passpasspass", RoleAdmin); err != nil {
		t.Fatal(err)
	}
	cookies := loginCookies(t, h, "alice", "passpasspass")

	called := false
	protected := h.RequireAuth(func(w http.ResponseWriter, r *http.Request) {
		called = true
		u := UserFromContext(r.Context())
		if u == nil || u.Username != "alice" {
			t.Errorf("user not on context")
		}
	})
	req := httptest.NewRequest(http.MethodGet, "/api/stats", nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rr := httptest.NewRecorder()
	protected(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d", rr.Code)
	}
	if !called {
		t.Errorf("inner handler never called")
	}
}

func TestRequireAuthEnforcesCSRFOnUnsafeMethods(t *testing.T) {
	svc := newTestService(t)
	h := NewHandler(svc)
	if _, err := svc.CreateUser("alice", "passpasspass", RoleAdmin); err != nil {
		t.Fatal(err)
	}
	cookies := loginCookies(t, h, "alice", "passpasspass")
	csrf := findCookie(cookies, CSRFCookieName).Value

	mk := func(method, csrfHeader string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/api/users", strings.NewReader(`{}`))
		for _, c := range cookies {
			req.AddCookie(c)
		}
		if csrfHeader != "" {
			req.Header.Set(CSRFHeaderName, csrfHeader)
		}
		rr := httptest.NewRecorder()
		h.RequireAuth(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})(rr, req)
		return rr
	}

	if rr := mk(http.MethodPost, ""); rr.Code != http.StatusForbidden {
		t.Errorf("POST without CSRF header: status %d, want 403", rr.Code)
	}
	if rr := mk(http.MethodPost, "wrong"); rr.Code != http.StatusForbidden {
		t.Errorf("POST with bad CSRF: status %d, want 403", rr.Code)
	}
	if rr := mk(http.MethodPost, csrf); rr.Code != http.StatusOK {
		t.Errorf("POST with valid CSRF: status %d, want 200", rr.Code)
	}
	// GET is safe-method, no CSRF required.
	if rr := mk(http.MethodGet, ""); rr.Code != http.StatusOK {
		t.Errorf("GET without CSRF: status %d, want 200", rr.Code)
	}
}

func TestRequireAdminRejectsViewer(t *testing.T) {
	svc := newTestService(t)
	h := NewHandler(svc)
	if _, err := svc.CreateUser("v", "passpasspass", RoleViewer); err != nil {
		t.Fatal(err)
	}
	cookies := loginCookies(t, h, "v", "passpasspass")

	req := httptest.NewRequest(http.MethodGet, "/api/users", nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rr := httptest.NewRecorder()
	h.RequireAdmin(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("viewer accessing admin route: status %d, want 403", rr.Code)
	}
}

func TestLogoutClearsSession(t *testing.T) {
	svc := newTestService(t)
	h := NewHandler(svc)
	if _, err := svc.CreateUser("alice", "passpasspass", RoleAdmin); err != nil {
		t.Fatal(err)
	}
	cookies := loginCookies(t, h, "alice", "passpasspass")
	sess := findCookie(cookies, SessionCookieName).Value
	csrf := findCookie(cookies, CSRFCookieName).Value

	req := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	req.Header.Set(CSRFHeaderName, csrf)
	rr := httptest.NewRecorder()
	h.RequireAuth(h.handleLogout)(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("logout status %d: %s", rr.Code, rr.Body.String())
	}
	if _, _, err := svc.ValidateSession(sess); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("session not deleted after logout: %v", err)
	}
}

func TestMeReturnsCurrentUser(t *testing.T) {
	svc := newTestService(t)
	h := NewHandler(svc)
	if _, err := svc.CreateUser("alice", "passpasspass", RoleAdmin); err != nil {
		t.Fatal(err)
	}
	cookies := loginCookies(t, h, "alice", "passpasspass")

	req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rr := httptest.NewRecorder()
	h.RequireAuth(h.handleMe)(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d", rr.Code)
	}
	var resp struct {
		User *User `json:"user"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.User == nil || resp.User.Username != "alice" {
		t.Errorf("unexpected /me response: %s", rr.Body.String())
	}
}
