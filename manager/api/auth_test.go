package api

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thinkaliker/labassistant/manager/auditor"
	"github.com/thinkaliker/labassistant/manager/settings"
)

func newAuthDeps(t *testing.T, password string) Deps {
	t.Helper()
	dir := t.TempDir()
	set, err := settings.Load(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if password != "" {
		if err := set.SetPassword("admin", password); err != nil {
			t.Fatal(err)
		}
	}
	aud, err := auditor.Open(filepath.Join(dir, "audit.log"), 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	return Deps{Settings: set, Sessions: NewSessions(), Aud: aud}
}

// postPassword sends a change-password request through the full router, so the auth
// middleware runs exactly as it does in production.
func postPassword(t *testing.T, d Deps, body string, auth func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("POST", "/api/v1/auth/password", strings.NewReader(body))
	if auth != nil {
		auth(r)
	}
	w := httptest.NewRecorder()
	Router(d).ServeHTTP(w, r)
	return w
}

func withSession(tok string) func(*http.Request) {
	return func(r *http.Request) { r.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok}) }
}

func TestChangePasswordEndsOtherSessions(t *testing.T) {
	d := newAuthDeps(t, "old")
	mine, other := d.Sessions.Create(), d.Sessions.Create()

	w := postPassword(t, d, `{"currentPassword":"old","newPassword":"new"}`, withSession(mine))
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204: %s", w.Code, w.Body.String())
	}
	if !d.Settings.CheckLogin("admin", "new") || d.Settings.CheckLogin("admin", "old") {
		t.Error("password not replaced")
	}
	if !d.Sessions.Valid(mine) {
		t.Error("the session that changed the password was signed out")
	}
	if d.Sessions.Valid(other) {
		t.Error("another session survived the password change")
	}
	if a := d.Aud.List(1); len(a) == 0 || a[0].Type != "password_changed" {
		t.Errorf("audit = %+v, want a password_changed entry", a)
	}
}

func TestChangePasswordRefusals(t *testing.T) {
	d := newAuthDeps(t, "old")
	sess := d.Sessions.Create()
	_, plain, err := d.Settings.AddToken("ci", true)
	if err != nil {
		t.Fatal(err)
	}
	bearer := func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+plain) }

	cases := []struct {
		name string
		body string
		auth func(*http.Request)
		want int
	}{
		{"unauthenticated", `{"currentPassword":"old","newPassword":"new"}`, nil, http.StatusUnauthorized},
		{"api token", `{"currentPassword":"old","newPassword":"new"}`, bearer, http.StatusForbidden},
		{"wrong current", `{"currentPassword":"nope","newPassword":"new"}`, withSession(sess), http.StatusForbidden},
		{"empty new", `{"currentPassword":"old","newPassword":""}`, withSession(sess), http.StatusBadRequest},
		{"bad json", `{`, withSession(sess), http.StatusBadRequest},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if w := postPassword(t, d, c.body, c.auth); w.Code != c.want {
				t.Errorf("status = %d, want %d: %s", w.Code, c.want, w.Body.String())
			}
			if !d.Settings.CheckLogin("admin", "old") {
				t.Fatal("a refused request changed the password")
			}
		})
	}
}

// With no password set the dashboard can't set the first one: anyone can reach an open-mode
// dashboard, so that would let the first visitor lock everyone else out.
func TestChangePasswordOpenMode(t *testing.T) {
	d := newAuthDeps(t, "")
	w := postPassword(t, d, `{"currentPassword":"","newPassword":"new"}`, nil)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", w.Code, w.Body.String())
	}
	if d.Settings.AuthConfigured() {
		t.Error("open mode: a password got set")
	}
}
