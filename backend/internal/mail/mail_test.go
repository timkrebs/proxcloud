package mail

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

func TestLogMailerWritesToWriterNotSlog(t *testing.T) {
	// Capture slog output separately to prove the token never lands there.
	var slogBuf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&slogBuf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	var out bytes.Buffer
	m := LogMailer{W: &out}

	const token = "SECRET-TOKEN-abc123"
	msg := BuildInviteEmail(InviteParams{
		To:             "invitee@example.com",
		FrontendOrigin: "https://portal.example",
		Token:          token,
		TenantName:     "Acme",
		ScopeLabel:     "Acme / Web",
		Role:           "contributor",
		InviterName:    "Owner",
	})
	if err := m.Send(context.Background(), msg); err != nil {
		t.Fatalf("Send: %v", err)
	}

	got := out.String()
	tests := []struct {
		name, want string
	}{
		{"dev banner", "--- DEV MAILER ---"},
		{"recipient", "invitee@example.com"},
		{"accept link", "https://portal.example/invite/" + token},
		{"raw token", token},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(got, tc.want) {
				t.Fatalf("LogMailer output missing %q; got:\n%s", tc.want, got)
			}
		})
	}

	// The raw token must NOT have leaked into the slog logger.
	if strings.Contains(slogBuf.String(), token) {
		t.Fatalf("invite token leaked into slog output:\n%s", slogBuf.String())
	}
}

func TestAcceptURL(t *testing.T) {
	tests := []struct {
		name, origin, token, want string
	}{
		{"no trailing slash", "https://p.example", "tok", "https://p.example/invite/tok"},
		{"trailing slash trimmed", "https://p.example/", "tok", "https://p.example/invite/tok"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := AcceptURL(tc.origin, tc.token); got != tc.want {
				t.Fatalf("AcceptURL(%q,%q) = %q, want %q", tc.origin, tc.token, got, tc.want)
			}
		})
	}
}

func TestBuildInviteEmailNoTokenInSubject(t *testing.T) {
	const token = "opaque-token-xyz"
	msg := BuildInviteEmail(InviteParams{
		To: "x@example.com", FrontendOrigin: "https://p.example", Token: token,
		TenantName: "Acme", Role: "reader",
	})
	// The token belongs only in the link (bodies), never the subject.
	if strings.Contains(msg.Subject, token) {
		t.Fatalf("token leaked into subject: %q", msg.Subject)
	}
	if !strings.Contains(msg.TextBody, token) || !strings.Contains(msg.HTMLBody, token) {
		t.Fatal("accept link (with token) missing from message bodies")
	}
}
