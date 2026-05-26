package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
)

// Cookie names. Kept here so the JS in the dashboard can hard-code the
// CSRF cookie name (it's read client-side to populate X-CSRF-Token).
const (
	SessionCookieName = "war_session"
	CSRFCookieName    = "war_csrf"
	CSRFHeaderName    = "X-CSRF-Token"
)

type ctxKey int

const (
	ctxUserKey ctxKey = iota
)

// UserFromContext returns the authenticated user stored on the request
// context by RequireAuth, or nil if the request was anonymous.
func UserFromContext(ctx context.Context) *User {
	if v, ok := ctx.Value(ctxUserKey).(*User); ok {
		return v
	}
	return nil
}

// Handler is the HTTP-facing wrapper around Service. Split from Service
// so the service is testable without touching net/http.
type Handler struct {
	svc *Service
}

// NewHandler wraps svc.
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Service returns the underlying service. Exposed for tests and
// for callers that need to perform direct operations.
func (h *Handler) Service() *Service { return h.svc }

// RegisterRoutes wires every auth/user route onto mux. The caller is
// responsible for separately wrapping protected APIs with RequireAuth.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/auth/login", h.handleLogin)
	mux.HandleFunc("/api/auth/logout", h.RequireAuth(h.handleLogout))
	mux.HandleFunc("/api/auth/me", h.RequireAuth(h.handleMe))
	mux.HandleFunc("/api/auth/csrf", h.handleCSRF)

	mux.HandleFunc("/api/users", h.RequireAdmin(h.handleUsersCollection))
	mux.HandleFunc("/api/users/", h.RequireAuth(h.handleUserItem))
}

// RequireAuth ensures a valid session is present on the request and
// stashes the user on the context. It also enforces CSRF on unsafe
// methods using the double-submit cookie pattern.
//
// No-account mode: if zero users exist in the database, every request
// is allowed through with no user attached. This lets a freshly
// installed instance be configured from the UI before any account is
// created. The mode automatically disappears as soon as the first
// user is created.
func (h *Handler) RequireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.noAccountMode() {
			next(w, r)
			return
		}
		u := h.lookupUser(r)
		if u == nil {
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		if !isSafeMethod(r.Method) {
			if !h.csrfOK(r) {
				writeError(w, http.StatusForbidden, "csrf token missing or invalid")
				return
			}
		}
		ctx := context.WithValue(r.Context(), ctxUserKey, u)
		next(w, r.WithContext(ctx))
	}
}

// RequireAdmin is RequireAuth plus a role check. In no-account mode
// the admin check is also bypassed: anonymous callers are effectively
// implicit administrators until the first real account is created.
func (h *Handler) RequireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return h.RequireAuth(func(w http.ResponseWriter, r *http.Request) {
		if h.noAccountMode() {
			next(w, r)
			return
		}
		u := UserFromContext(r.Context())
		if u == nil || u.Role != RoleAdmin {
			writeError(w, http.StatusForbidden, "admin role required")
			return
		}
		next(w, r)
	})
}

// noAccountMode reports whether the auth system currently has zero
// users. Errors from the database are treated as "users exist" (fail
// closed) so a transient DB problem doesn't silently disable auth.
func (h *Handler) noAccountMode() bool {
	n, err := h.svc.CountUsers()
	if err != nil {
		return false
	}
	return n == 0
}

// lookupUser tries to resolve the session cookie to a user. Returns nil
// on any failure (no cookie, expired, disabled, etc.). Errors are
// swallowed because callers turn nil into a 401 anyway.
func (h *Handler) lookupUser(r *http.Request) *User {
	c, err := r.Cookie(SessionCookieName)
	if err != nil || c.Value == "" {
		return nil
	}
	u, _, err := h.svc.ValidateSession(c.Value)
	if err != nil {
		return nil
	}
	return u
}

// csrfOK implements the double-submit cookie check: the CSRF cookie
// value must match the X-CSRF-Token header. Comparison is
// constant-time so a leaked timing oracle can't be used to guess the
// token byte-by-byte.
func (h *Handler) csrfOK(r *http.Request) bool {
	c, err := r.Cookie(CSRFCookieName)
	if err != nil || c.Value == "" {
		return false
	}
	hdr := r.Header.Get(CSRFHeaderName)
	if hdr == "" {
		return false
	}
	return constantTimeEqual(c.Value, hdr)
}

func isSafeMethod(m string) bool {
	switch m {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	default:
		return false
	}
}

// ---- HTTP handlers --------------------------------------------------

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (h *Handler) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	ip := clientIP(r)
	u, err := h.svc.Authenticate(req.Username, req.Password)
	if err != nil {
		// Audit the failed attempt with the supplied username so an
		// operator can investigate brute-force patterns later.
		_ = h.svc.Audit(nil, "login_failed", req.Username, ip, err.Error())
		// Map all auth failures to a generic 401 to avoid leaking
		// "user exists / wrong password" vs "user disabled".
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	token, _, err := h.svc.CreateSession(u.ID, ip, r.UserAgent())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "session error")
		return
	}
	csrfToken, err := generateToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "csrf error")
		return
	}
	h.setSessionCookie(w, token)
	h.setCSRFCookie(w, csrfToken)
	_ = h.svc.Audit(&u.ID, "login", u.Username, ip, "")
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"user":       u,
		"csrf_token": csrfToken,
	})
}

func (h *Handler) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if c, err := r.Cookie(SessionCookieName); err == nil {
		_ = h.svc.DeleteSession(c.Value)
	}
	h.clearCookie(w, SessionCookieName)
	h.clearCookie(w, CSRFCookieName)
	u := UserFromContext(r.Context())
	if u != nil {
		_ = h.svc.Audit(&u.ID, "logout", u.Username, clientIP(r), "")
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) handleMe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	u := UserFromContext(r.Context())
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"user":            u,
		"no_account_mode": h.noAccountMode(),
	})
}

// handleCSRF issues (or refreshes) a CSRF cookie. Useful so the JS
// client can prime the cookie before showing the login form (so the
// first non-GET request after login already has a token to echo back).
func (h *Handler) handleCSRF(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	// Re-use an existing token if one is already set; otherwise mint
	// a fresh one. This keeps the value stable across reloads.
	token := ""
	if c, err := r.Cookie(CSRFCookieName); err == nil {
		token = c.Value
	}
	if token == "" {
		t, err := generateToken()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "csrf error")
			return
		}
		token = t
		h.setCSRFCookie(w, token)
	}
	writeJSON(w, http.StatusOK, map[string]string{"csrf_token": token})
}

// ---- user management ------------------------------------------------

type createUserRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Role     string `json:"role"`
}

func (h *Handler) handleUsersCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		users, err := h.svc.ListUsers()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"users": users})
	case http.MethodPost:
		var req createUserRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		if req.Role == "" {
			req.Role = RoleViewer
		}
		u, err := h.svc.CreateUser(req.Username, req.Password, req.Role)
		if err != nil {
			writeError(w, statusForError(err), err.Error())
			return
		}
		actor := UserFromContext(r.Context())
		var actorID *int64
		if actor != nil {
			actorID = &actor.ID
		}
		_ = h.svc.Audit(actorID, "user_create", u.Username, clientIP(r), "role="+u.Role)
		writeJSON(w, http.StatusCreated, map[string]interface{}{"user": u})
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

type updateUserRequest struct {
	Role     *string `json:"role,omitempty"`
	Disabled *bool   `json:"disabled,omitempty"`
	Password *string `json:"password,omitempty"`
}

func (h *Handler) handleUserItem(w http.ResponseWriter, r *http.Request) {
	// Path is /api/users/{id} or /api/users/me/password.
	rest := strings.TrimPrefix(r.URL.Path, "/api/users/")
	actor := UserFromContext(r.Context())

	if rest == "me/password" {
		h.handleSelfPassword(w, r, actor)
		return
	}

	// Everything else requires admin.
	if actor == nil || actor.Role != RoleAdmin {
		writeError(w, http.StatusForbidden, "admin role required")
		return
	}

	id, err := strconv.ParseInt(rest, 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusNotFound, "not found")
		return
	}

	switch r.Method {
	case http.MethodGet:
		u, err := h.svc.GetUser(id)
		if err != nil {
			writeError(w, statusForError(err), err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"user": u})
	case http.MethodPatch:
		var req updateUserRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		// Don't let an admin disable themselves and lock everyone out.
		if req.Disabled != nil && *req.Disabled && id == actor.ID {
			writeError(w, http.StatusBadRequest, "cannot disable yourself")
			return
		}
		u, err := h.svc.UpdateUser(id, req.Role, req.Disabled)
		if err != nil {
			writeError(w, statusForError(err), err.Error())
			return
		}
		if req.Password != nil {
			if err := h.svc.ChangePassword(id, *req.Password); err != nil {
				writeError(w, statusForError(err), err.Error())
				return
			}
		}
		_ = h.svc.Audit(&actor.ID, "user_update", u.Username, clientIP(r), "")
		writeJSON(w, http.StatusOK, map[string]interface{}{"user": u})
	case http.MethodDelete:
		if id == actor.ID {
			writeError(w, http.StatusBadRequest, "cannot delete yourself")
			return
		}
		u, _ := h.svc.GetUser(id)
		if err := h.svc.DeleteUser(id); err != nil {
			writeError(w, statusForError(err), err.Error())
			return
		}
		target := ""
		if u != nil {
			target = u.Username
		}
		_ = h.svc.Audit(&actor.ID, "user_delete", target, clientIP(r), "")
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

type selfPasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

func (h *Handler) handleSelfPassword(w http.ResponseWriter, r *http.Request, actor *User) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if actor == nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	var req selfPasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if _, err := h.svc.Authenticate(actor.Username, req.CurrentPassword); err != nil {
		writeError(w, http.StatusUnauthorized, "current password is incorrect")
		return
	}
	if err := h.svc.ChangePassword(actor.ID, req.NewPassword); err != nil {
		writeError(w, statusForError(err), err.Error())
		return
	}
	_ = h.svc.Audit(&actor.ID, "password_change", actor.Username, clientIP(r), "")
	// The session was just invalidated by ChangePassword; tell the
	// browser to drop its cookies so the next request lands on the
	// login screen.
	h.clearCookie(w, SessionCookieName)
	h.clearCookie(w, CSRFCookieName)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ---- cookies & helpers ---------------------------------------------

func (h *Handler) setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   h.svc.cookieSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(h.svc.sessionTTL.Seconds()),
	})
}

// setCSRFCookie sets the double-submit cookie. It is intentionally NOT
// HttpOnly so the page's JavaScript can read it and echo the value in
// the X-CSRF-Token header.
func (h *Handler) setCSRFCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     CSRFCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: false,
		Secure:   h.svc.cookieSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(h.svc.sessionTTL.Seconds()),
	})
}

func (h *Handler) clearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		HttpOnly: name == SessionCookieName,
		Secure:   h.svc.cookieSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

// clientIP returns the connecting peer's address. Honours
// X-Forwarded-For if present (we trust it because deployments are
// expected to sit behind a reverse proxy).
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.Index(xff, ","); i >= 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func statusForError(err error) int {
	switch {
	case errors.Is(err, ErrUserNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrUserExists):
		return http.StatusConflict
	case errors.Is(err, ErrWeakPassword), errors.Is(err, ErrInvalidRole):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}
