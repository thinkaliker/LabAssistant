package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"
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
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: tok, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, MaxAge: int(sessionTTL.Seconds()),
	})
	writeJSON(w, http.StatusOK, map[string]string{"username": req.Username})
}

func (d Deps) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		d.Sessions.Delete(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1})
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
