// Package mail sends transactional email — in Phase 5, the invitation accept
// link (ADR-0013). It is deliberately tiny and driver-agnostic: a Mailer
// interface with two implementations, LogMailer (dev default, writes to stdout)
// and SMTPMailer (net/smtp). main.go selects SMTPMailer when SMTP_HOST is set,
// else LogMailer. The invite-email builder lives here (invite.go) so the accept
// link is constructed in exactly one place.
//
// Security note (ADR-0013): a raw invite token is proof of mailbox control, so
// it must never reach the structured/access logs. LogMailer therefore writes to
// its io.Writer directly and never through slog.
package mail

import "context"

// Message is a single email. HTMLBody may be empty (text-only).
type Message struct {
	To       string
	Subject  string
	TextBody string
	HTMLBody string
}

// Mailer sends a Message. Implementations must respect ctx cancellation/timeout
// for any network I/O and must never log message bodies (they carry secrets).
type Mailer interface {
	Send(ctx context.Context, m Message) error
}
