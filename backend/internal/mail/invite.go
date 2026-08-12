package mail

import (
	"fmt"
	"html"
	"strings"
)

// InviteParams are the inputs to BuildInviteEmail. Chunk B fills these after it
// mints the token and loads the tenant/scope; the raw Token appears only inside
// the accept link (never persisted, never logged via slog).
type InviteParams struct {
	To             string // recipient email (the invited address)
	FrontendOrigin string // accept-link base, e.g. https://portal.example (no trailing slash)
	Token          string // raw invite token (base64url); goes in the link only
	TenantName     string // tenant the invite grants access to
	ScopeLabel     string // human scope, e.g. "Acme" (tenant) or "Acme / Web" (project)
	Role           string // "owner" | "contributor" | "reader"
	InviterName    string // "" when unknown/system
}

// AcceptURL builds the invite accept link: FRONTEND_ORIGIN + /invite/{token}.
// It is the single place that constructs this URL. A base64url token is already
// URL-path-safe, so no escaping is applied.
func AcceptURL(frontendOrigin, token string) string {
	return strings.TrimRight(frontendOrigin, "/") + "/invite/" + token
}

// BuildInviteEmail renders the invitation email (subject + text + HTML) with the
// accept link. User-supplied fields are HTML-escaped in the HTML body.
func BuildInviteEmail(p InviteParams) Message {
	tenant := p.TenantName
	if tenant == "" {
		tenant = "a Proxcloud tenant"
	}
	scope := p.ScopeLabel
	if scope == "" {
		scope = tenant
	}
	url := AcceptURL(p.FrontendOrigin, p.Token)

	inviter := ""
	if p.InviterName != "" {
		inviter = fmt.Sprintf(" by %s", p.InviterName)
	}
	roleLine := ""
	if p.Role != "" {
		roleLine = fmt.Sprintf(" as %s", p.Role)
	}

	subject := fmt.Sprintf("You're invited to %s on Proxcloud", tenant)

	text := fmt.Sprintf(
		"You have been invited%s to join %s%s on Proxcloud.\n\n"+
			"Accept your invitation:\n%s\n\n"+
			"If you did not expect this invitation you can ignore this email.\n",
		inviter, scope, roleLine, url)

	htmlBody := fmt.Sprintf(
		"<p>You have been invited%s to join <strong>%s</strong>%s on Proxcloud.</p>"+
			"<p><a href=\"%s\">Accept your invitation</a></p>"+
			"<p>Or paste this link into your browser:<br>%s</p>"+
			"<p style=\"color:#666\">If you did not expect this invitation you can ignore this email.</p>",
		html.EscapeString(inviter), html.EscapeString(scope), html.EscapeString(roleLine),
		html.EscapeString(url), html.EscapeString(url))

	return Message{To: p.To, Subject: subject, TextBody: text, HTMLBody: htmlBody}
}
