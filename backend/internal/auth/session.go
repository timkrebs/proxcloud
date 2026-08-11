// Package auth implements the portal's single-admin login: bcrypt password
// verification and a stateless HMAC-signed session cookie. The Proxmox
// token is never involved — this guards the portal itself.
package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	// CookieName is the session cookie the browser holds.
	CookieName = "proxcloud_session"
	// sessionTTL slides: RequireSession re-issues the cookie once it is
	// past half its lifetime, so active users stay signed in and a stolen
	// cookie dies within a day of inactivity. Stateless design: logout
	// clears only the browser copy (documented limitation).
	sessionTTL = 24 * time.Hour
)

type sessionPayload struct {
	User string `json:"u"`
	Exp  int64  `json:"exp"`
}

// Sessions signs and verifies session cookies.
type Sessions struct {
	secret []byte
	secure bool
	now    func() time.Time
}

// NewSessions creates a session signer. secure controls the cookie's Secure
// attribute (true behind TLS).
func NewSessions(secret []byte, secure bool) *Sessions {
	return &Sessions{secret: secret, secure: secure, now: time.Now}
}

// Issue returns a Set-Cookie ready session cookie for user.
func (s *Sessions) Issue(user string) *http.Cookie {
	payload, _ := json.Marshal(sessionPayload{User: user, Exp: s.now().Add(sessionTTL).Unix()})
	body := base64.RawURLEncoding.EncodeToString(payload)
	return &http.Cookie{
		Name:     CookieName,
		Value:    body + "." + s.sign(body),
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   s.secure,
		MaxAge:   int(sessionTTL.Seconds()),
	}
}

// Clear returns an expired cookie that removes the session.
func (s *Sessions) Clear() *http.Cookie {
	return &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   s.secure,
		MaxAge:   -1,
	}
}

// Verify returns the authenticated username, or an error if the cookie is
// missing, forged, or expired.
func (s *Sessions) Verify(r *http.Request) (string, error) {
	c, err := r.Cookie(CookieName)
	if err != nil {
		return "", fmt.Errorf("no session cookie")
	}
	user, _, err := s.verifyValue(c.Value)
	return user, err
}

func (s *Sessions) verifyValue(value string) (user string, exp int64, err error) {
	body, sig, ok := strings.Cut(value, ".")
	if !ok {
		return "", 0, fmt.Errorf("malformed session cookie")
	}
	if !hmac.Equal([]byte(s.sign(body)), []byte(sig)) {
		return "", 0, fmt.Errorf("invalid session signature")
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return "", 0, fmt.Errorf("malformed session payload")
	}
	var p sessionPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return "", 0, fmt.Errorf("malformed session payload")
	}
	if s.now().Unix() >= p.Exp {
		return "", 0, fmt.Errorf("session expired")
	}
	return p.User, p.Exp, nil
}

// VerifyRefresh is Verify plus a signal that the cookie is past half its
// lifetime and should be re-issued.
func (s *Sessions) VerifyRefresh(r *http.Request) (user string, refresh bool, err error) {
	c, err := r.Cookie(CookieName)
	if err != nil {
		return "", false, fmt.Errorf("no session cookie")
	}
	user, exp, err := s.verifyValue(c.Value)
	if err != nil {
		return "", false, err
	}
	refresh = time.Until(time.Unix(exp, 0)) < sessionTTL/2
	return user, refresh, nil
}

func (s *Sessions) sign(body string) string {
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(body))
	return hex.EncodeToString(mac.Sum(nil))
}
