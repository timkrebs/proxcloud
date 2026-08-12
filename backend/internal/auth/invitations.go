package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/auditz"
	"github.com/timkrebs9/proxcloud/backend/internal/store"
)

// Invite-accept sentinel errors carried out of the WithTx so the caller maps
// them to the documented statuses without leaking which check failed.
var (
	// errInviteGone: the invite vanished/expired/was already accepted between the
	// pre-tx read and the tx re-read → generic 404 (enumeration-safe).
	errInviteGone = errors.New("auth: invite gone or expired")
	// errInviteRaced: MarkInvitationAccepted lost the single-use race → 409.
	errInviteRaced = errors.New("auth: invite already accepted (raced)")
)

// inviteNotFound is the single generic 404 returned by validate/accept for any
// unknown/expired/used token (or an unknown email on validate) — no enumeration.
func inviteNotFound() *types.APIError {
	return &types.APIError{Code: "not_found", Message: "Invitation not found.", Status: http.StatusNotFound}
}

// inviteUnusable reports whether an invite may no longer be validated/accepted:
// it is nil, already accepted, or past its expiry.
func inviteUnusable(inv *store.Invitation, now time.Time) bool {
	return inv == nil || inv.AcceptedAt != nil || now.After(inv.ExpiresAt)
}

// ValidateInvite serves GET /api/auth/invitations/{token} (Public). It looks the
// invite up by SHA-256(token); an unknown/expired/accepted token — or an invite
// whose tenant/project has since gone — returns the SAME generic 404, so a caller
// cannot enumerate valid tokens. A live invite returns InvitationDetails (never
// the token). Rate-limited per-IP.
func (h *Handler) ValidateInvite(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if h.Limiter != nil && !h.Limiter.Allow(ip) {
		h.logger().Warn("validate invite rate limited", "ip", ip)
		writeErr(w, rateLimited())
		return
	}
	token := chi.URLParam(r, "token")
	inv, err := h.Store.GetInvitationByTokenHash(r.Context(), hashToken(token))
	if err != nil || inviteUnusable(inv, time.Now()) {
		writeErr(w, inviteNotFound())
		return
	}

	tenantName, scopeLabel, ok := h.resolveInviteScope(r.Context(), inv)
	if !ok {
		writeErr(w, inviteNotFound()) // scope gone → indistinguishable 404
		return
	}

	// RequiresAccount: no user exists for the invite email yet.
	requiresAccount := false
	if _, uerr := h.Store.GetUserByEmail(r.Context(), inv.Email); errors.Is(uerr, store.ErrNotFound) {
		requiresAccount = true
	} else if uerr != nil {
		h.logger().Error("validate invite: get user by email", "err", uerr)
		writeErr(w, internalErr())
		return
	}

	// SignedInMatches: the caller's current session belongs to the invite email.
	signedInMatches := false
	if id, verr := h.Sessions.Verify(r.Context(), r); verr == nil && strings.EqualFold(id.Email, inv.Email) {
		signedInMatches = true
	}

	writeJSON(w, http.StatusOK, types.InvitationDetails{
		Email:           inv.Email,
		TenantName:      tenantName,
		ScopeType:       inv.ScopeType,
		ScopeLabel:      scopeLabel,
		Role:            inv.Role,
		ExpiresAt:       inv.ExpiresAt,
		RequiresAccount: requiresAccount,
		SignedInMatches: signedInMatches,
	})
}

// AcceptInvite serves POST /api/auth/invitations/{token}/accept (Public). It
// resolves the acceptor, then in ONE WithTx creates-or-attaches the user, grants
// the membership FROM THE INVITE ROW's scope/role (never the request body), and
// stamps accepted_at under a single-use guard; on success it issues a rotated
// session bound to the invite's tenant. Response 204 + proxcloud_session. Audited
// invitation.accept (auditz Begin before / Finalize after; fail-closed on intent
// failure). Rate-limited per-IP; the Argon2id hash for a new account is bounded by
// the bcrypt semaphore.
func (h *Handler) AcceptInvite(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if h.Limiter != nil && !h.Limiter.Allow(ip) {
		h.logger().Warn("accept invite rate limited", "ip", ip)
		writeErr(w, rateLimited())
		return
	}
	var req types.AcceptInvitationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, &types.APIError{Code: "invalid_request", Message: "Request body must be JSON.", Status: http.StatusBadRequest})
		return
	}
	token := chi.URLParam(r, "token")
	tokenHash := hashToken(token)

	// Enumeration-safe pre-read: an unusable token is a generic 404 here, before
	// any work or account-existence probe.
	inv, err := h.Store.GetInvitationByTokenHash(r.Context(), tokenHash)
	if err != nil || inviteUnusable(inv, time.Now()) {
		writeErr(w, inviteNotFound())
		return
	}

	// Resolve the invite's tenant (for the active-tenant set + audit tenant). A
	// gone scope is an enumeration-safe 404, same as validate.
	tenantID, _, _, ok := h.resolveInviteTenant(r.Context(), inv)
	if !ok {
		writeErr(w, inviteNotFound())
		return
	}

	// Decide the acceptor OUTSIDE the tx. Signed-in identity (if any) is authoritative.
	var signedIn *Identity
	if id, verr := h.Sessions.Verify(r.Context(), r); verr == nil {
		signedIn = id
	}

	var (
		createNewUser bool
		newUserHash   string
		newDisplay    string
		attachUserID  string // existing signed-in user to attach
	)
	switch {
	case signedIn != nil && strings.EqualFold(signedIn.Email, inv.Email):
		attachUserID = signedIn.UserID
	case signedIn != nil:
		// Signed in as a different email — refuse (sign out first).
		writeErr(w, &types.APIError{Code: "email_mismatch", Message: "You are signed in as a different account. Sign out, then accept.", Status: http.StatusConflict})
		return
	default:
		// Not signed in: an existing account must sign in first; otherwise create one.
		existing, uerr := h.Store.GetUserByEmail(r.Context(), inv.Email)
		if uerr == nil && existing != nil {
			writeErr(w, &types.APIError{Code: "account_exists", Message: "An account already exists for this email. Sign in first, then accept.", Status: http.StatusConflict})
			return
		}
		if uerr != nil && !errors.Is(uerr, store.ErrNotFound) {
			h.logger().Error("accept invite: get user by email", "err", uerr)
			writeErr(w, internalErr())
			return
		}
		createNewUser = true
		newDisplay = strings.TrimSpace(req.DisplayName)
		if newDisplay == "" {
			writeErr(w, &types.APIError{Code: "invalid_request", Message: "A display name is required.", Status: http.StatusBadRequest})
			return
		}
		if perr := validatePasswordStrength(req.Password); perr != nil {
			writeErr(w, perr)
			return
		}
		// Bound the Argon2id work behind the shared bcrypt semaphore (a flood of
		// accepts cannot exhaust CPU/RAM) — before the audit intent, so a failed
		// intent still mutates nothing.
		hash, herr := h.hashPassword(req.Password)
		if herr != nil {
			h.logger().Error("accept invite: hash password", "err", herr)
			writeErr(w, internalErr())
			return
		}
		newUserHash = hash
	}

	// Fail-closed audit intent BEFORE the mutation. Actor is the attaching user
	// when known; for a brand-new account the id is resolved in the tx and recorded
	// in the finalize detail. An intent-insert failure refuses the accept.
	rec := h.Auditz
	if rec == nil {
		rec = &auditz.Recorder{Store: h.Store, Log: h.logger()}
	}
	pending, aerr := rec.Begin(r.Context(), auditz.Intent{
		Action:      "invitation.accept",
		ActorUserID: attachUserID,
		TenantID:    tenantID,
		TargetType:  "invitation",
		TargetID:    inv.ID,
		IP:          ipPtr(r),
	})
	if aerr != nil {
		h.logger().Error("audit intent for invitation.accept failed — accept refused", "err", aerr)
		writeErr(w, internalErr())
		return
	}

	// The atomic accept: re-read under the tx, create-or-attach, grant from the row,
	// single-use stamp. Any error rolls the whole thing back (no partial accept).
	var acceptedUserID string
	err = h.Store.WithTx(r.Context(), func(tx store.Store) error {
		cur, rerr := tx.GetInvitationByTokenHash(r.Context(), tokenHash)
		if rerr != nil {
			return errInviteGone
		}
		if cur.AcceptedAt != nil {
			return errInviteRaced
		}
		if time.Now().After(cur.ExpiresAt) {
			return errInviteGone
		}

		userID := attachUserID
		if createNewUser {
			u, cerr := tx.CreateUser(r.Context(), store.CreateUserParams{
				Email:        cur.Email,
				DisplayName:  newDisplay,
				PasswordHash: newUserHash,
				PasswordAlgo: AlgoArgon2id,
			})
			if cerr != nil {
				return cerr
			}
			userID = u.ID
		}

		// Membership is granted from the ROW's scope_type/scope_id/role — never from
		// the request. A tampered client body cannot change the granted authority.
		if _, merr := tx.CreateMembership(r.Context(), store.CreateMembershipParams{
			UserID:    userID,
			ScopeType: cur.ScopeType,
			ScopeID:   cur.ScopeID,
			Role:      cur.Role,
		}); merr != nil {
			return merr
		}

		won, serr := tx.MarkInvitationAccepted(r.Context(), cur.ID)
		if serr != nil {
			return serr
		}
		if !won {
			return errInviteRaced // lost the single-use race → nothing committed
		}
		acceptedUserID = userID
		return nil
	})
	if err != nil {
		switch {
		case errors.Is(err, errInviteGone):
			pending.Finalize(r.Context(), "denied", map[string]any{"status": http.StatusNotFound})
			writeErr(w, inviteNotFound())
		case errors.Is(err, errInviteRaced):
			pending.Finalize(r.Context(), "denied", map[string]any{"status": http.StatusConflict})
			writeErr(w, &types.APIError{Code: "conflict", Message: "This invitation was already accepted.", Status: http.StatusConflict})
		case errors.Is(err, store.ErrConflict):
			// A raced create for a NEW account: a concurrent accept (or an
			// out-of-band signup) created the user for this email between the
			// pre-tx GetUserByEmail miss and the in-tx CreateUser, tripping the
			// unique-email constraint. That is not a 500 — the account now exists,
			// so route the caller to sign in, mirroring the pre-tx account_exists.
			pending.Finalize(r.Context(), "denied", map[string]any{"status": http.StatusConflict})
			writeErr(w, &types.APIError{Code: "account_exists", Message: "An account already exists for this email. Sign in first, then accept.", Status: http.StatusConflict})
		default:
			h.logger().Error("accept invite tx", "err", err)
			pending.Finalize(r.Context(), "error", map[string]any{"status": http.StatusInternalServerError})
			writeErr(w, internalErr())
		}
		return
	}

	// Issue a rotated session bound to the invite's tenant. If the acceptor was
	// already signed in as this user, retire the prior session (rotation).
	cookie, ierr := h.issueSessionForTenant(r.Context(), acceptedUserID, tenantID, r)
	if ierr != nil {
		h.logger().Error("accept invite: issue session", "err", ierr)
		pending.Finalize(r.Context(), "error", map[string]any{"status": http.StatusInternalServerError})
		writeErr(w, internalErr())
		return
	}
	if signedIn != nil && signedIn.UserID == acceptedUserID {
		if rerr := h.Sessions.Revoke(r.Context(), signedIn.SessionID); rerr != nil {
			h.logger().Warn("accept invite: revoke prior session", "err", rerr) // non-fatal
		}
	}
	if h.Limiter != nil {
		h.Limiter.Reset(ip)
	}
	http.SetCookie(w, cookie)
	pending.Finalize(r.Context(), "success", map[string]any{
		"status":  http.StatusNoContent,
		"user_id": acceptedUserID,
		"email":   inv.Email,
	})
	h.logger().Info("invitation accepted", "user_id", acceptedUserID, "tenant", tenantID)
	w.WriteHeader(http.StatusNoContent)
}

// resolveInviteScope resolves an invite's tenant name and human scope label. ok
// is false when the referenced tenant/project no longer exists (→ generic 404).
func (h *Handler) resolveInviteScope(ctx context.Context, inv *store.Invitation) (tenantName, scopeLabel string, ok bool) {
	_, tenantName, scopeLabel, ok = h.resolveInviteTenant(ctx, inv)
	return tenantName, scopeLabel, ok
}

// resolveInviteTenant resolves the invite's owning tenant id, tenant name, and
// scope label from the row's scope. ok is false when the scope's tenant/project
// is gone.
func (h *Handler) resolveInviteTenant(ctx context.Context, inv *store.Invitation) (tenantID, tenantName, scopeLabel string, ok bool) {
	switch inv.ScopeType {
	case "tenant":
		t, err := h.Store.GetTenantByID(ctx, inv.ScopeID)
		if err != nil {
			return "", "", "", false
		}
		return t.ID, t.Name, t.Name, true
	case "project":
		p, err := h.Store.GetProjectByID(ctx, inv.ScopeID)
		if err != nil {
			return "", "", "", false
		}
		t, err := h.Store.GetTenantByID(ctx, p.TenantID)
		if err != nil {
			return "", "", "", false
		}
		return t.ID, t.Name, t.Name + " / " + p.Name, true
	default:
		return "", "", "", false
	}
}

// issueSessionForTenant issues a rotated session for userID and binds it to
// tenantID as the active tenant, returning the Set-Cookie. The new session's id
// is resolved by verifying the freshly-minted cookie (the opaque token is not
// otherwise returned); a set-active-tenant failure is non-fatal (the session is
// usable, just without a preselected tenant).
func (h *Handler) issueSessionForTenant(ctx context.Context, userID, tenantID string, r *http.Request) (*http.Cookie, error) {
	cookie, err := h.Sessions.Issue(ctx, userID, r)
	if err != nil {
		return nil, err
	}
	if tenantID == "" {
		return cookie, nil
	}
	probe := &http.Request{Header: http.Header{}}
	probe.AddCookie(cookie)
	id, verr := h.Sessions.Verify(ctx, probe)
	if verr != nil {
		return cookie, nil // session is valid; active tenant simply not preset
	}
	if serr := h.Store.SetSessionActiveTenant(ctx, id.SessionID, &tenantID); serr != nil {
		h.logger().Warn("accept invite: set active tenant failed", "err", serr)
	}
	return cookie, nil
}

// ipPtr returns the caller's IP as *string for the audit row, or nil.
func ipPtr(r *http.Request) *string {
	if ip := clientIP(r); ip != "" {
		return &ip
	}
	return nil
}
