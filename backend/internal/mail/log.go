package mail

import (
	"context"
	"fmt"
	"io"
	"os"
)

// LogMailer is the dev-default Mailer: it prints the full message — including
// the accept link — to W (os.Stdout when nil) as a clearly-marked block, so a
// developer running without SMTP can copy the link from their console.
//
// It writes to W directly and NEVER through slog: the raw invite token in the
// body must not land in the structured or access logs (ADR-0013). Do not swap
// this for a slog call.
type LogMailer struct {
	W io.Writer // defaults to os.Stdout
}

// Send writes the message to W. It never returns an error (a broken stdout is
// not worth failing an invite over), but the io.Writer contract is preserved.
func (m LogMailer) Send(_ context.Context, msg Message) error {
	w := m.W
	if w == nil {
		w = os.Stdout
	}
	body := msg.TextBody
	if body == "" {
		body = msg.HTMLBody
	}
	_, err := fmt.Fprintf(w,
		"\n--- DEV MAILER ---\nTo: %s\nSubject: %s\n\n%s\n--- END DEV MAILER ---\n",
		msg.To, msg.Subject, body)
	return err
}
