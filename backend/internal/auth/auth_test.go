package auth

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
)

func testHandler(t *testing.T) *Handler {
	t.Helper()
	hash, err := ResolveHash("", "correct-horse")
	if err != nil {
		t.Fatalf("ResolveHash: %v", err)
	}
	return &Handler{
		Sessions:     NewSessions([]byte(strings.Repeat("s", 32)), false),
		AdminUser:    "admin",
		PasswordHash: hash,
		Log:          slog.New(slog.NewTextHandler(testWriter{t}, nil)),
	}
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) { w.t.Log(strings.TrimSpace(string(p))); return len(p), nil }

func TestLogin(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantCookie bool
		wantCode   string
	}{
		{"valid credentials", `{"username":"admin","password":"correct-horse"}`, http.StatusNoContent, true, ""},
		{"wrong password", `{"username":"admin","password":"wrong"}`, http.StatusUnauthorized, false, "unauthenticated"},
		{"wrong username", `{"username":"root","password":"correct-horse"}`, http.StatusUnauthorized, false, "unauthenticated"},
		{"empty body fields", `{}`, http.StatusUnauthorized, false, "unauthenticated"},
		{"malformed json", `{not json`, http.StatusBadRequest, false, "invalid_request"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := testHandler(t)
			req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(tt.body))
			rec := httptest.NewRecorder()
			h.Login(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tt.wantStatus, rec.Body)
			}
			cookies := rec.Result().Cookies()
			if tt.wantCookie {
				if len(cookies) != 1 || cookies[0].Name != CookieName {
					t.Fatalf("expected session cookie, got %v", cookies)
				}
				c := cookies[0]
				if !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/" {
					t.Errorf("cookie attributes wrong: HttpOnly=%v SameSite=%v Path=%q", c.HttpOnly, c.SameSite, c.Path)
				}
			} else if len(cookies) != 0 {
				t.Fatalf("expected no cookie, got %v", cookies)
			}
			if tt.wantCode != "" {
				var env types.ErrorEnvelope
				if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
					t.Fatalf("error body not an envelope: %v", err)
				}
				if env.Error.Code != tt.wantCode {
					t.Errorf("error code = %q, want %q", env.Error.Code, tt.wantCode)
				}
			}
		})
	}
}

func TestSessionVerify(t *testing.T) {
	s := NewSessions([]byte(strings.Repeat("k", 32)), false)

	valid := s.Issue("admin").Value
	forged := valid[:len(valid)-4] + "beef"
	otherSigner := NewSessions([]byte(strings.Repeat("x", 32)), false)
	wrongKey := otherSigner.Issue("admin").Value

	expiredSessions := NewSessions([]byte(strings.Repeat("k", 32)), false)
	expiredSessions.now = func() time.Time { return time.Now().Add(-8 * 24 * time.Hour) }
	expired := expiredSessions.Issue("admin").Value

	tests := []struct {
		name    string
		cookie  string
		wantErr bool
		user    string
	}{
		{"valid", valid, false, "admin"},
		{"forged signature", forged, true, ""},
		{"signed with different key", wrongKey, true, ""},
		{"expired", expired, true, ""},
		{"garbage", "not-a-session", true, ""},
		{"empty", "", true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.cookie != "" {
				req.AddCookie(&http.Cookie{Name: CookieName, Value: tt.cookie})
			}
			user, err := s.Verify(req)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if user != tt.user {
				t.Errorf("user = %q, want %q", user, tt.user)
			}
		})
	}
}

func TestMeAndLogout(t *testing.T) {
	h := testHandler(t)

	// signed in
	req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	req.AddCookie(&http.Cookie{Name: CookieName, Value: h.Sessions.Issue("admin").Value})
	rec := httptest.NewRecorder()
	h.Me(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("me status = %d", rec.Code)
	}
	var me types.Me
	if err := json.Unmarshal(rec.Body.Bytes(), &me); err != nil || me.Username != "admin" {
		t.Fatalf("me body = %s, err %v", rec.Body, err)
	}

	// signed out
	rec = httptest.NewRecorder()
	h.Me(rec, httptest.NewRequest(http.MethodGet, "/api/auth/me", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("me without session = %d, want 401", rec.Code)
	}

	// logout clears the cookie
	rec = httptest.NewRecorder()
	h.Logout(rec, httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil))
	cs := rec.Result().Cookies()
	if len(cs) != 1 || cs[0].MaxAge != -1 {
		t.Fatalf("logout cookie = %v, want MaxAge -1", cs)
	}
}

func TestRequireSession(t *testing.T) {
	h := testHandler(t)
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	mw := h.RequireSession(next)

	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/resources", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated = %d, want 401", rec.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/resources", nil)
	req.AddCookie(&http.Cookie{Name: CookieName, Value: h.Sessions.Issue("admin").Value})
	rec = httptest.NewRecorder()
	mw.ServeHTTP(rec, req)
	if rec.Code != http.StatusTeapot {
		t.Fatalf("authenticated = %d, want passthrough 418", rec.Code)
	}
}

func TestResolveHash(t *testing.T) {
	if _, err := ResolveHash("not-bcrypt", ""); err == nil {
		t.Error("invalid hash accepted")
	}
	h, err := ResolveHash("", "pw")
	if err != nil || !CheckPassword(h, "pw") || CheckPassword(h, "other") {
		t.Errorf("plaintext hashing broken: %v", err)
	}
}
