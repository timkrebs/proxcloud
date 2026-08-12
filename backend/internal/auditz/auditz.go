// Package auditz records fail-closed audit rows for state-changing operations
// that live OUTSIDE the tenant-scoped AuditOnMutation choke-point — the
// platform-admin mutations (tenant create, tenant quota update) and, in Phase 5,
// the account-level security mutations (password change, TOTP) in internal/auth.
//
// It mirrors the middleware's contract exactly (ADR-0012 §3): a durable intent
// row (outcome "pending") is written BEFORE the mutation — an intent failure
// refuses the mutation, so nothing is ever mutated unlogged — and the row's
// outcome/detail are finalized AFTER — a finalize failure is logged, never
// surfaced, because the intent row is already a durable record.
//
// It depends only on store.Store (never on net/http, auth, or authz), so both
// internal/handlers and internal/auth can import it without an import cycle. The
// only two audit mutations remain store.InsertAuditIntent + store.FinalizeAudit,
// so who/what/when stays immutable.
package auditz

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/timkrebs9/proxcloud/backend/internal/store"
)

// finalizeTimeout bounds the post-mutation outcome write.
const finalizeTimeout = 5 * time.Second

// Recorder writes audit rows through a store.Store. Log is optional (defaults to
// slog.Default). Construct one per call from the request-scoped store, or hold a
// single instance — it carries no per-request state.
type Recorder struct {
	Store store.Store
	Log   *slog.Logger
}

func (r *Recorder) log() *slog.Logger {
	if r.Log != nil {
		return r.Log
	}
	return slog.Default()
}

// Intent describes the audit columns known BEFORE the mutation runs. Empty
// string fields map to a NULL column (actor/tenant/project/target are all
// nullable). TargetID stays empty for creates whose id is not yet known — the
// resolved id belongs in the finalize detail (mirroring guest.create's vmid).
type Intent struct {
	Action      string
	ActorUserID string
	TenantID    string
	ProjectID   string
	TargetType  string
	TargetID    string
	IP          *string
}

// Pending is the durable intent handle returned by Begin. Its outcome/detail are
// written exactly once, by Finalize.
type Pending struct {
	rec *Recorder
	id  string
}

// ID is the intent row's id (for correlation/tests).
func (p *Pending) ID() string { return p.id }

// Begin writes the fail-closed intent row (outcome "pending") BEFORE the
// mutation and returns a Pending handle. On error the caller MUST refuse the
// mutation (respond 500) and NOT proceed — nothing may be mutated unlogged.
func (r *Recorder) Begin(ctx context.Context, in Intent) (*Pending, error) {
	id, err := r.Store.InsertAuditIntent(ctx, store.AuditIntent{
		ActorUserID: nonEmpty(in.ActorUserID),
		TenantID:    nonEmpty(in.TenantID),
		ProjectID:   nonEmpty(in.ProjectID),
		Action:      in.Action,
		TargetType:  nonEmpty(in.TargetType),
		TargetID:    nonEmpty(in.TargetID),
		IP:          in.IP,
	})
	if err != nil {
		return nil, err
	}
	return &Pending{rec: r, id: id}, nil
}

// Finalize writes the one-way outcome + jsonb detail on the intent row AFTER the
// mutation. A finalize failure is LOGGED, never returned — the intent row is
// already durable, so the caller's response must not change. It detaches from
// request cancellation so a client disconnect can never drop the outcome; the
// intent row remains durable ("pending") regardless.
func (p *Pending) Finalize(ctx context.Context, outcome string, detail map[string]any) {
	var raw []byte
	if len(detail) > 0 {
		if b, err := json.Marshal(detail); err == nil {
			raw = b
		}
	}
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), finalizeTimeout)
	defer cancel()
	if err := p.rec.Store.FinalizeAudit(fctx, p.id, outcome, raw); err != nil {
		p.rec.log().Error("auditz: finalize failed (intent row durable; response unchanged)",
			"audit_id", p.id, "outcome", outcome, "err", err)
	}
}

// OutcomeForStatus maps an HTTP status to the audit outcome vocabulary, matching
// AuditOnMutation: 2xx (and an unwritten 0) → success, 4xx → denied, else error.
func OutcomeForStatus(status int) string {
	switch {
	case status == 0 || (status >= 200 && status < 300):
		return "success"
	case status >= 400 && status < 500:
		return "denied"
	default:
		return "error"
	}
}

// nonEmpty returns &s when s is non-empty, else nil (for the nullable columns).
func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
