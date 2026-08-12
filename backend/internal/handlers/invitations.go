package handlers

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/authz"
	"github.com/timkrebs9/proxcloud/backend/internal/httpserver"
	"github.com/timkrebs9/proxcloud/backend/internal/mail"
	"github.com/timkrebs9/proxcloud/backend/internal/store"
)

// inviteTokenBytes is the raw invite-token length (256 bits, ADR-0013 §1): a
// crypto/rand value only ever stored as its SHA-256 hash, mirroring session
// tokens. The token is an opaque unguessable lookup key with no authority of its
// own — scope and role live in the row.
const inviteTokenBytes = 32

// defaultInvitationTTL is the fallback when Deps.InvitationTTL is unset (tests
// and defensive; main.go always injects cfg.InvitationTTL, default 72h).
const defaultInvitationTTL = 72 * time.Hour

// mintInviteToken returns a fresh base64url token and its SHA-256 hex hash. Only
// the hash is persisted; the raw token goes into the emailed accept link.
func mintInviteToken() (token, tokenHash string, err error) {
	raw := make([]byte, inviteTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	return token, hashInviteToken(token), nil
}

// hashInviteToken is the SHA-256 hex of a raw invite token — the only form
// persisted (invitations.token_hash), matching the session-token pattern.
func hashInviteToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// CreateInvitation serves POST /api/tenants/{tenantId}/invitations (Owner). It
// validates email + role, resolves the scope (a project scope must belong to the
// active tenant, else 404), caps the granted role at the inviter's effective role
// (no privilege escalation → 403), mints a hashed 256-bit token, persists the row
// (superseding any pending invite for the same email+scope), and emails the
// accept link. Response 201 Invitation (never the token). Audited invitation.create
// via AuditOnMutation; the detail is enriched with email/role/scope.
func (d *Deps) CreateInvitation(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIdentity(w, r)
	if !ok || !d.requireStore(w) {
		return
	}
	var req types.CreateInvitationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpserver.WriteError(w, badRequest("Request body must be JSON with email, scopeType, scopeId, role."))
		return
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if email == "" || !strings.Contains(email, "@") {
		httpserver.WriteError(w, badRequest("A valid email is required."))
		return
	}
	role := strings.TrimSpace(req.Role)
	if role != authz.RoleOwnerStr && role != authz.RoleContributorStr && role != authz.RoleReaderStr {
		httpserver.WriteError(w, badRequest("role must be owner, contributor, or reader."))
		return
	}
	// Cap the granted role at the inviter's effective role — an Owner can grant at
	// most Owner; a lesser role can never mint a higher one (privilege escalation).
	// Defense-in-depth / forward-compat: this route is Owner-gated, so today the
	// inviter's EffectiveRole is always "owner" and the cap is unreachable. It
	// becomes load-bearing the moment a project-scoped invite route (where the
	// caller may be a project Owner, not a tenant Owner) is added.
	if !authz.RoleAtLeast(authz.ParseRole(id.EffectiveRole), authz.ParseRole(role)) {
		httpserver.WriteError(w, &types.APIError{
			Code:    "forbidden",
			Message: "You cannot grant a role higher than your own.",
			Status:  http.StatusForbidden,
		})
		return
	}

	tenantID := id.ActiveTenantID
	tenant, err := d.Store.GetTenantByID(r.Context(), tenantID)
	if errors.Is(err, store.ErrNotFound) {
		httpserver.WriteError(w, notFound("Tenant not found."))
		return
	}
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}

	// Resolve the scope. A tenant scope must be this tenant; a project scope must
	// be a project OF this tenant (cross-tenant scopeId → 404, no existence leak).
	scopeType := strings.TrimSpace(req.ScopeType)
	scopeID := strings.TrimSpace(req.ScopeID)
	var scopeLabel string
	switch scopeType {
	case "tenant":
		if scopeID == "" {
			scopeID = tenantID
		}
		if scopeID != tenantID {
			httpserver.WriteError(w, notFound("Scope not found."))
			return
		}
		scopeLabel = tenant.Name
	case "project":
		proj, perr := d.Store.GetProjectByID(r.Context(), scopeID)
		if errors.Is(perr, store.ErrNotFound) {
			httpserver.WriteError(w, notFound("Project not found."))
			return
		}
		if perr != nil {
			httpserver.WriteError(w, perr)
			return
		}
		if proj.TenantID != tenantID {
			httpserver.WriteError(w, notFound("Project not found.")) // cross-tenant → 404
			return
		}
		scopeLabel = tenant.Name + " / " + proj.Name
	default:
		httpserver.WriteError(w, badRequest("scopeType must be tenant or project."))
		return
	}

	token, tokenHash, err := mintInviteToken()
	if err != nil {
		d.logger().Error("invitation: mint token", "err", err)
		httpserver.WriteError(w, &types.APIError{Code: "internal", Message: "internal server error", Status: http.StatusInternalServerError})
		return
	}

	ttl := d.InvitationTTL
	if ttl <= 0 {
		ttl = defaultInvitationTTL
	}
	inviter := id.UserID

	// Supersede-then-insert must be atomic, so the store contract requires WithTx.
	var inv *store.Invitation
	err = d.Store.WithTx(r.Context(), func(tx store.Store) error {
		i, cerr := tx.CreateInvitation(r.Context(), store.CreateInvitationParams{
			TokenHash: tokenHash,
			Email:     email,
			ScopeType: scopeType,
			ScopeID:   scopeID,
			Role:      role,
			InvitedBy: &inviter,
			ExpiresAt: time.Now().Add(ttl),
		})
		if cerr != nil {
			return cerr
		}
		inv = i
		return nil
	})
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			// A token_hash collision is astronomically unlikely; surface it honestly
			// rather than as a 500 so a retry (fresh token) succeeds.
			httpserver.WriteError(w, &types.APIError{Code: "conflict", Message: "Could not create the invitation, please retry.", Status: http.StatusConflict})
			return
		}
		httpserver.WriteError(w, err)
		return
	}

	// Deliver the accept link. Delivery failure (or a missing FRONTEND_ORIGIN) is
	// logged but never fails the request — the invite row exists and re-inviting
	// supersedes it with a fresh token.
	inviterName := d.resolveInviterName(r.Context(), inviter, id.Email)
	d.sendInviteEmailToken(r.Context(), email, token, tenant.Name, scopeLabel, role, inviterName, inv.ID)

	// Enrich the audit detail (best-effort; the one row is guaranteed regardless).
	authz.Annotate(r.Context(), "email", email)
	authz.Annotate(r.Context(), "role", role)
	authz.Annotate(r.Context(), "scope", scopeType)

	httpserver.WriteJSON(w, http.StatusCreated, types.Invitation{
		ID:         inv.ID,
		Email:      inv.Email,
		ScopeType:  inv.ScopeType,
		ScopeID:    inv.ScopeID,
		ScopeLabel: scopeLabel,
		Role:       inv.Role,
		InvitedBy:  inviterName,
		ExpiresAt:  inv.ExpiresAt,
		CreatedAt:  inv.CreatedAt,
		Status:     "pending",
	})
}

// sendInviteEmailToken builds and sends the accept-link email. It NEVER fails
// the caller: a nil mailer, empty FRONTEND_ORIGIN, or send error is logged only
// (the raw token stays out of slog — only the id/email are logged). It holds the
// raw token (never persisted) so the link is built exactly once.
func (d *Deps) sendInviteEmailToken(ctx context.Context, to, token, tenantName, scopeLabel, role, inviterName, invitationID string) {
	if d.FrontendOrigin == "" {
		d.logger().Warn("invitation created but FRONTEND_ORIGIN is empty — the accept link will be unusable",
			"invitation_id", invitationID, "email", to)
	}
	if d.Mailer == nil {
		d.logger().Warn("invitation created but no mailer is configured — no email sent",
			"invitation_id", invitationID, "email", to)
		return
	}
	msg := mail.BuildInviteEmail(mail.InviteParams{
		To:             to,
		FrontendOrigin: d.FrontendOrigin,
		Token:          token,
		TenantName:     tenantName,
		ScopeLabel:     scopeLabel,
		Role:           role,
		InviterName:    inviterName,
	})
	sendCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := d.Mailer.Send(sendCtx, msg); err != nil {
		// Honest: the invite row is durable; the Owner sees it pending and can
		// re-send. We do not fail the request over a mail-transport error.
		d.logger().Error("invitation email send failed (invite persisted; re-invite to resend)",
			"invitation_id", invitationID, "email", to, "err", err)
	}
}

// resolveInviterName returns the inviter's display name (falling back to their
// email) for the invite email and the Invitation.InvitedBy field.
func (d *Deps) resolveInviterName(ctx context.Context, userID, fallbackEmail string) string {
	if u, err := d.Store.GetUserByID(ctx, userID); err == nil {
		if strings.TrimSpace(u.DisplayName) != "" {
			return u.DisplayName
		}
		if u.Email != "" {
			return u.Email
		}
	}
	return fallbackEmail
}

// ListInvitations serves GET /api/tenants/{tenantId}/invitations (Owner): every
// pending (unaccepted) invite for the tenant AND its projects — two scope queries,
// no per-project N+1. Each carries a resolved scope label and computed status
// (pending/expired). Never returns tokens.
func (d *Deps) ListInvitations(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIdentity(w, r)
	if !ok || !d.requireStore(w) {
		return
	}
	tenantID := id.ActiveTenantID

	tenant, err := d.Store.GetTenantByID(r.Context(), tenantID)
	if errors.Is(err, store.ErrNotFound) {
		httpserver.WriteError(w, notFound("Tenant not found."))
		return
	}
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}

	invs, projs, err := d.tenantPendingInvitations(r.Context(), tenantID)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	projByID := make(map[string]store.Project, len(projs))
	for i := range projs {
		projByID[projs[i].ID] = projs[i]
	}

	// Resolve inviter identities in one batch (no N+1).
	inviterIDs := make([]string, 0, len(invs))
	for i := range invs {
		if invs[i].InvitedBy != nil {
			inviterIDs = append(inviterIDs, *invs[i].InvitedBy)
		}
	}
	users := map[string]store.User{}
	if len(inviterIDs) > 0 {
		if u, uerr := d.Store.ListUsersByIDs(r.Context(), inviterIDs); uerr == nil {
			users = u
		} else {
			d.logger().Warn("list invitations: resolve inviter identities failed", "err", uerr)
		}
	}

	now := time.Now()
	out := make([]types.Invitation, 0, len(invs))
	for i := range invs {
		inv := invs[i]
		label := tenant.Name
		if inv.ScopeType == "project" {
			if p, ok := projByID[inv.ScopeID]; ok {
				label = tenant.Name + " / " + p.Name
			}
		}
		status := "pending"
		if now.After(inv.ExpiresAt) {
			status = "expired"
		}
		invitedBy := ""
		if inv.InvitedBy != nil {
			if u, ok := users[*inv.InvitedBy]; ok {
				if strings.TrimSpace(u.DisplayName) != "" {
					invitedBy = u.DisplayName
				} else {
					invitedBy = u.Email
				}
			}
		}
		out = append(out, types.Invitation{
			ID:         inv.ID,
			Email:      inv.Email,
			ScopeType:  inv.ScopeType,
			ScopeID:    inv.ScopeID,
			ScopeLabel: label,
			Role:       inv.Role,
			InvitedBy:  invitedBy,
			ExpiresAt:  inv.ExpiresAt,
			CreatedAt:  inv.CreatedAt,
			Status:     status,
		})
	}
	httpserver.WriteJSON(w, http.StatusOK, out)
}

// tenantPendingInvitations returns every pending (unaccepted) invite for the
// tenant AND its projects — the two scope queries the Owner list needs, with no
// per-project N+1 — plus the tenant's projects (for scope-label resolution).
// Shared by ListInvitations.
func (d *Deps) tenantPendingInvitations(ctx context.Context, tenantID string) ([]store.Invitation, []store.Project, error) {
	projs, err := d.Store.ListProjectsByTenant(ctx, tenantID)
	if err != nil {
		return nil, nil, err
	}
	projIDs := make([]string, 0, len(projs))
	for i := range projs {
		projIDs = append(projIDs, projs[i].ID)
	}
	tenantInvs, err := d.Store.ListPendingInvitationsByScopes(ctx, "tenant", []string{tenantID})
	if err != nil {
		return nil, nil, err
	}
	projInvs, err := d.Store.ListPendingInvitationsByScopes(ctx, "project", projIDs)
	if err != nil {
		return nil, nil, err
	}
	return append(tenantInvs, projInvs...), projs, nil
}

// invitationInTenant reports whether inv is a still-pending invite that belongs
// to tenantID — a tenant-scope invite for the tenant itself, or a project-scope
// invite for one of the tenant's projects. It authorizes revoke in O(1)
// (load-by-id) without scanning the tenant's whole pending list. An accepted
// invite is treated as absent (it is not a pending invite of this tenant).
func (d *Deps) invitationInTenant(ctx context.Context, inv *store.Invitation, tenantID string) bool {
	if inv == nil || inv.AcceptedAt != nil {
		return false
	}
	switch inv.ScopeType {
	case "tenant":
		return inv.ScopeID == tenantID
	case "project":
		proj, err := d.Store.GetProjectByID(ctx, inv.ScopeID)
		return err == nil && proj.TenantID == tenantID
	default:
		return false
	}
}

// RevokeInvitation serves DELETE /api/tenants/{tenantId}/invitations/{invitationId}
// (Owner). It hard-deletes a pending invite of THIS tenant; an invitationId that
// is not one of this tenant's pending invites (including another tenant's) → 404,
// no existence leak. Response 204. Audited invitation.revoke via AuditOnMutation.
func (d *Deps) RevokeInvitation(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIdentity(w, r)
	if !ok || !d.requireStore(w) {
		return
	}
	tenantID := id.ActiveTenantID
	invID := chi.URLParam(r, "invitationId")

	// O(1) authorization: load the invite by id, then confirm it belongs to this
	// tenant (the tenant itself or one of its projects). A cross-tenant or unknown
	// id is indistinguishable → 404, no existence leak.
	inv, err := d.Store.GetInvitationByID(r.Context(), invID)
	if errors.Is(err, store.ErrNotFound) {
		httpserver.WriteError(w, notFound("Invitation not found."))
		return
	}
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	if !d.invitationInTenant(r.Context(), inv, tenantID) {
		httpserver.WriteError(w, notFound("Invitation not found."))
		return
	}

	if err := d.Store.DeleteInvitation(r.Context(), invID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpserver.WriteError(w, notFound("Invitation not found."))
			return
		}
		httpserver.WriteError(w, err)
		return
	}
	authz.Annotate(r.Context(), "invitation_id", invID)
	w.WriteHeader(http.StatusNoContent)
}
