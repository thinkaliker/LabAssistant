package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/thinkaliker/labassistant/manager/settings"
)

const sessionCookie = "la_session"
const sessionTTL = 12 * time.Hour

// Sessions is an in-memory session store for the dashboard login.
type Sessions struct {
	mu sync.Mutex
	m  map[string]time.Time
}

// NewSessions creates an empty session store.
func NewSessions() *Sessions {
	return &Sessions{m: map[string]time.Time{}}
}

// Create returns a new session token valid for the TTL.
func (s *Sessions) Create() string {
	raw := make([]byte, 24)
	_, _ = rand.Read(raw)
	tok := hex.EncodeToString(raw)
	s.mu.Lock()
	s.m[tok] = time.Now().Add(sessionTTL)
	s.mu.Unlock()
	return tok
}

// Valid reports whether a token is live.
func (s *Sessions) Valid(tok string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.m[tok]
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		delete(s.m, tok)
		return false
	}
	return true
}

// Delete invalidates a token.
func (s *Sessions) Delete(tok string) {
	s.mu.Lock()
	delete(s.m, tok)
	s.mu.Unlock()
}

// DeleteExcept invalidates every session but keep, returning how many it ended.
func (s *Sessions) DeleteExcept(keep string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for tok := range s.m {
		if tok != keep {
			delete(s.m, tok)
			n++
		}
	}
	return n
}

// authMiddleware enforces authentication on /api/v1 except the public login and session
// status endpoints. The dashboard calls /api/v1/auth/session before logging in to decide
// whether to show the login screen, so it must be reachable while unauthenticated. When no
// password is configured the manager runs in dev-open mode (all requests allowed).
func (d Deps) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/login" || r.URL.Path == "/api/v1/auth/session" || !d.Settings.AuthConfigured() {
			next.ServeHTTP(w, r)
			return
		}
		if c, err := r.Cookie(sessionCookie); err == nil && d.Sessions.Valid(c.Value) {
			next.ServeHTTP(w, r)
			return
		}
		if tok := bearerToken(r); tok != "" && d.Settings.CheckToken(tok) {
			next.ServeHTTP(w, r)
			return
		}
		writeErr(w, http.StatusUnauthorized, "unauthorized", "authentication required")
	})
}

// auditReadAllowed gates audit-log reads. A human session always qualifies; API tokens must
// be explicitly granted audit access (off by default — entries can hold sensitive details).
// Dev-open mode (no auth configured) allows reads like every other endpoint.
func (d Deps) auditReadAllowed(r *http.Request) bool {
	if !d.Settings.AuthConfigured() {
		return true
	}
	if c, err := r.Cookie(sessionCookie); err == nil && d.Sessions.Valid(c.Value) {
		return true
	}
	if tok := bearerToken(r); tok != "" && d.Settings.TokenHasAuditAccess(tok) {
		return true
	}
	return false
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if after, ok := strings.CutPrefix(h, "Bearer "); ok {
		return after
	}
	return ""
}

func (d Deps) login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "invalid JSON")
		return
	}
	if !d.Settings.CheckLogin(req.Username, req.Password) {
		writeErr(w, http.StatusUnauthorized, "unauthorized", "invalid credentials")
		return
	}
	tok := d.Sessions.Create()
	// Secure only when this hop is TLS: a plain-HTTP dashboard (or one behind a TLS-terminating
	// proxy on loopback) would otherwise never get the cookie back.
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: tok, Path: "/", HttpOnly: true, Secure: r.TLS != nil,
		SameSite: http.SameSiteLaxMode, MaxAge: int(sessionTTL.Seconds()),
	})
	writeJSON(w, http.StatusOK, map[string]string{"username": req.Username})
}

// changePassword replaces the dashboard password and signs out every other session, so a
// password change also locks out anyone holding an old login. It needs a dashboard session,
// not an API token: a token that could set the password could turn itself into a full login.
func (d Deps) changePassword(w http.ResponseWriter, r *http.Request) {
	if !d.Settings.AuthConfigured() {
		writeErr(w, http.StatusConflict, "no_password",
			"no password is set; set the first one with `manage.sh setpass` on the manager host")
		return
	}
	c, err := r.Cookie(sessionCookie)
	if err != nil || !d.Sessions.Valid(c.Value) {
		writeErr(w, http.StatusForbidden, "session_required", "changing the password needs a dashboard sign-in, not an API token")
		return
	}
	var req struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "invalid JSON")
		return
	}
	switch err := d.Settings.ChangePassword(req.CurrentPassword, req.NewPassword); {
	case err == nil:
	case errors.Is(err, settings.ErrWrongPassword):
		writeErr(w, http.StatusForbidden, "wrong_password", "current password is incorrect")
		return
	case errors.Is(err, settings.ErrEmptyPassword):
		writeErr(w, http.StatusBadRequest, "bad_request", "new password cannot be empty")
		return
	case errors.Is(err, settings.ErrPasswordTooLong):
		writeErr(w, http.StatusBadRequest, "bad_request", "new password is too long (72 bytes at most)")
		return
	case errors.Is(err, settings.ErrPasswordChanged):
		writeErr(w, http.StatusConflict, "conflict", "the password was changed at the same time; sign in again and retry")
		return
	default:
		writeErr(w, http.StatusInternalServerError, "error", err.Error())
		return
	}
	ended := d.Sessions.DeleteExcept(c.Value)
	d.Aud.Record("password_changed", "", "user", "dashboard password changed",
		map[string]int{"sessionsEnded": ended})
	w.WriteHeader(http.StatusNoContent)
}

func (d Deps) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		d.Sessions.Delete(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, Secure: r.TLS != nil})
	w.WriteHeader(http.StatusNoContent)
}

// session reports auth status for the dashboard (whether auth is required and, if so,
// whether the caller is authenticated).
func (d Deps) session(w http.ResponseWriter, r *http.Request) {
	required := d.Settings.AuthConfigured()
	authed := !required
	if required {
		if c, err := r.Cookie(sessionCookie); err == nil && d.Sessions.Valid(c.Value) {
			authed = true
		} else if tok := bearerToken(r); tok != "" && d.Settings.CheckToken(tok) {
			authed = true
		}
	}
	resp := map[string]any{
		"authRequired":  required,
		"authenticated": authed,
		// instance changes on every manager restart; the dashboard polls this to notice a
		// restart underneath it and prompt the user to sign in again.
		"instance": d.Instance,
	}
	// Only disclose the username to authenticated callers; this endpoint is public so the
	// dashboard can detect whether a login is needed before any credentials are presented.
	if authed {
		resp["username"] = d.Settings.Username()
	}
	writeJSON(w, http.StatusOK, resp)
}

func (d Deps) listTokens(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, d.Settings.ListTokens())
}

func (d Deps) createToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string `json:"name"`
		AuditAccess bool   `json:"auditAccess"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	t, plain, err := d.Settings.AddToken(req.Name, req.AuditAccess)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "error", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": t.ID, "name": t.Name, "token": plain})
}

func (d Deps) revokeToken(w http.ResponseWriter, r *http.Request) {
	if !d.Settings.RevokeToken(r.PathValue("id")) {
		writeErr(w, http.StatusNotFound, "not_found", "token not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
