package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/httpserver"
	"github.com/timkrebs9/proxcloud/backend/internal/lifecycle"
	"github.com/timkrebs9/proxcloud/backend/internal/store"
)

// maxTTLPolicyCeilingSeconds is the absolute ceiling on a project's max_ttl (10
// years) — a sanity bound far above any real homelab TTL that keeps the value
// well within int64-nanosecond time.Duration range.
const maxTTLPolicyCeilingSeconds = 3650 * 24 * 60 * 60

// ttlToWire converts a stored TTL into its wire form (durations as seconds).
func ttlToWire(t *store.TTL) types.Ttl {
	return types.Ttl{
		ID:                      t.ID,
		TenantID:                t.TenantID,
		ProjectID:               t.ProjectID,
		VMID:                    t.VMID,
		ExpiresAt:               t.ExpiresAt,
		Action:                  t.Action,
		Warned24h:               t.Warned24h,
		Warned1h:                t.Warned1h,
		OriginalDurationSeconds: int64(t.OriginalDuration.Seconds()),
		CreatedAt:               t.CreatedAt,
		UpdatedAt:               t.UpdatedAt,
	}
}

// projectMaxTTL resolves a project's TTL ceiling: the stored policy's max, or the
// migration default (30 days) when no policy row exists. Any other store error is
// surfaced.
func (d *Deps) projectMaxTTL(r *http.Request, tenantID, projectID string) (time.Duration, error) {
	pol, err := d.Store.GetProjectTTLPolicy(r.Context(), tenantID, projectID)
	switch {
	case err == nil:
		return pol.MaxTTL, nil
	case errors.Is(err, store.ErrNotFound):
		return lifecycle.DefaultMaxTTL, nil
	default:
		return 0, err
	}
}

// GetGuestTTL serves GET …/guests/{node}/{type}/{vmid}/ttl (Reader): the guest's
// TTL, or 404 when none is set.
func (d *Deps) GetGuestTTL(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireIdentity(w, r); !ok || !d.requireStore(w) {
		return
	}
	ref, apiErr := guestRef(r)
	if apiErr != nil {
		httpserver.WriteError(w, apiErr)
		return
	}
	ttl, err := d.Store.GetTTL(r.Context(), ref.VMID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpserver.WriteError(w, notFound("No TTL set for this guest."))
			return
		}
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, ttlToWire(ttl))
}

// PutGuestTTL serves PUT …/guests/{node}/{type}/{vmid}/ttl (Contributor): create
// or replace the guest's TTL, then re-materialize its jobs (when the feature is
// active). action=delete requires a typed confirmName matching the guest's name —
// enforced server-side (a CSRF slip or mis-scripted client cannot arm a
// destructive TTL without naming the guest).
func (d *Deps) PutGuestTTL(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIdentity(w, r)
	if !ok || !d.requireStore(w) {
		return
	}
	ref, apiErr := guestRef(r)
	if apiErr != nil {
		httpserver.WriteError(w, apiErr)
		return
	}
	var req types.TtlRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpserver.WriteError(w, badRequest("Request body must be a JSON TtlRequest."))
		return
	}
	if req.Action != "stop" && req.Action != "delete" {
		httpserver.WriteError(w, badRequest(`action must be "stop" or "delete".`))
		return
	}
	if req.TtlSeconds <= 0 {
		httpserver.WriteError(w, badRequest("ttlSeconds must be greater than 0."))
		return
	}

	// Ownership is already tenant-verified by ResolveScope; read it for the guest's
	// project (the TTL's project_id) — a missing row is a 404, never invented.
	own, err := d.Store.GetOwnershipByVMID(r.Context(), ref.VMID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpserver.WriteError(w, notFound("Guest not found."))
			return
		}
		httpserver.WriteError(w, err)
		return
	}

	// Enforce the project ceiling: no TTL may exceed max_ttl (ADR-0020). Compare in
	// SECONDS before converting to a Duration, so a pathologically large ttlSeconds
	// cannot overflow int64 nanoseconds into a negative Duration that slips past the
	// check (and then produces an already-past ExpiresAt).
	maxTTL, err := d.projectMaxTTL(r, own.TenantID, own.ProjectID)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	if req.TtlSeconds > int64(maxTTL.Seconds()) {
		httpserver.WriteError(w, badRequest("ttlSeconds exceeds the project's maximum TTL."))
		return
	}
	dur := time.Duration(req.TtlSeconds) * time.Second

	// Typed-confirmation for the irreversible action, enforced server-side.
	if req.Action == "delete" {
		st, err := d.PVE.GuestStatus(r.Context(), ref)
		if err != nil {
			httpserver.WriteError(w, err)
			return
		}
		if req.ConfirmName == "" || req.ConfirmName != st.Name {
			httpserver.WriteError(w, badRequest("A delete TTL requires confirmName matching the guest's name."))
			return
		}
	}

	userID := id.UserID
	params := store.UpsertTTLParams{
		TenantID:         own.TenantID,
		ProjectID:        own.ProjectID,
		VMID:             ref.VMID,
		ExpiresAt:        time.Now().Add(dur),
		Action:           req.Action,
		OriginalDuration: dur,
		CreatedBy:        &userID,
	}
	// Persist the TTL and re-materialize its jobs in ONE transaction, so a
	// materialize failure rolls back the upsert too — no "saved but no jobs" state.
	var ttl *store.TTL
	err = d.Store.WithTx(r.Context(), func(txs store.Store) error {
		var e error
		if ttl, e = txs.UpsertTTL(r.Context(), params); e != nil {
			return e
		}
		if d.TTLEnabled && d.TTL != nil {
			return d.TTL.MaterializeForGuestWith(r.Context(), txs, *own)
		}
		return nil
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, ttlToWire(ttl))
}

// DeleteGuestTTL serves DELETE …/guests/{node}/{type}/{vmid}/ttl (Contributor):
// clear the guest's TTL and cancel its ttl.* jobs (leaving any auto-shutdown jobs
// intact), atomically.
func (d *Deps) DeleteGuestTTL(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireIdentity(w, r); !ok || !d.requireStore(w) {
		return
	}
	ref, apiErr := guestRef(r)
	if apiErr != nil {
		httpserver.WriteError(w, apiErr)
		return
	}
	err := d.Store.WithTx(r.Context(), func(txs store.Store) error {
		if e := txs.DeleteTTL(r.Context(), ref.VMID); e != nil {
			return e
		}
		_, e := txs.CancelJobsForVMIDByPrefix(r.Context(), ref.VMID, "ttl.")
		return e
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpserver.WriteError(w, notFound("No TTL set for this guest."))
			return
		}
		httpserver.WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ExtendGuestTTL serves POST …/guests/{node}/{type}/{vmid}/ttl/extend
// (Contributor): extend the guest's expiry by one original_duration, capped at the
// project max_ttl, resetting the warning flags and rescheduling. The real user is
// the audited actor (via AuditOnMutation), not the scheduler.
func (d *Deps) ExtendGuestTTL(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireIdentity(w, r); !ok || !d.requireStore(w) {
		return
	}
	ref, apiErr := guestRef(r)
	if apiErr != nil {
		httpserver.WriteError(w, apiErr)
		return
	}
	own, err := d.Store.GetOwnershipByVMID(r.Context(), ref.VMID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpserver.WriteError(w, notFound("Guest not found."))
			return
		}
		httpserver.WriteError(w, err)
		return
	}
	ttl, err := d.Store.GetTTL(r.Context(), ref.VMID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpserver.WriteError(w, notFound("No TTL set for this guest."))
			return
		}
		httpserver.WriteError(w, err)
		return
	}

	var newExpiry time.Time
	if d.TTLEnabled && d.TTL != nil {
		newExpiry, err = d.TTL.ExtendTTL(r.Context(), *own, ttl)
		if err != nil {
			httpserver.WriteError(w, err)
			return
		}
	} else {
		// Feature off: update the expiry only (no jobs to reschedule), still capped.
		maxTTL, e := d.projectMaxTTL(r, own.TenantID, own.ProjectID)
		if e != nil {
			httpserver.WriteError(w, e)
			return
		}
		newExpiry = lifecycle.CapExtension(time.Now(), ttl.ExpiresAt, ttl.OriginalDuration, maxTTL)
		if e := d.Store.UpdateTTLExpiry(r.Context(), ref.VMID, newExpiry); e != nil {
			httpserver.WriteError(w, e)
			return
		}
	}
	httpserver.WriteJSON(w, http.StatusOK, types.TtlExtendResult{ExpiresAt: newExpiry})
}

// ListProjectTTLs serves GET …/projects/{projectId}/ttls (Reader): a project's
// TTLs ordered by expiry (the "expiring soon / expired" view).
func (d *Deps) ListProjectTTLs(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireIdentity(w, r); !ok || !d.requireStore(w) {
		return
	}
	projectID := chi.URLParam(r, "projectId")
	ttls, err := d.Store.ListTTLsByProject(r.Context(), projectID)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	out := make([]types.Ttl, 0, len(ttls))
	for i := range ttls {
		out = append(out, ttlToWire(&ttls[i]))
	}
	httpserver.WriteJSON(w, http.StatusOK, out)
}

// GetProjectTTLPolicy serves GET …/projects/{projectId}/ttl-policy (Reader): the
// stored policy, or the default (no default TTL, max 30 days) when none is set —
// never a 404, so the editor always has a policy to render.
func (d *Deps) GetProjectTTLPolicy(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIdentity(w, r)
	if !ok || !d.requireStore(w) {
		return
	}
	projectID := chi.URLParam(r, "projectId")
	pol, err := d.Store.GetProjectTTLPolicy(r.Context(), id.ActiveTenantID, projectID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpserver.WriteJSON(w, http.StatusOK, types.TtlPolicy{
				MaxTtlSeconds: int64(lifecycle.DefaultMaxTTL.Seconds()),
			})
			return
		}
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, ttlPolicyToWire(pol))
}

// PutProjectTTLPolicy serves PUT …/projects/{projectId}/ttl-policy (Owner —
// project governance, like project quota): set the default/max policy.
func (d *Deps) PutProjectTTLPolicy(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIdentity(w, r)
	if !ok || !d.requireStore(w) {
		return
	}
	projectID := chi.URLParam(r, "projectId")
	var req types.TtlPolicyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpserver.WriteError(w, badRequest("Request body must be a JSON TtlPolicyRequest."))
		return
	}
	if req.MaxTtlSeconds <= 0 {
		httpserver.WriteError(w, badRequest("maxTtlSeconds must be greater than 0."))
		return
	}
	// Absolute ceiling on the policy max itself, so a pathological value can't
	// overflow int64 nanoseconds when later read back as a time.Duration (and then
	// produce a negative/undefined ceiling that a TTL would silently pass).
	if req.MaxTtlSeconds > maxTTLPolicyCeilingSeconds {
		httpserver.WriteError(w, badRequest("maxTtlSeconds is unreasonably large (max 10 years)."))
		return
	}
	var defaultTTL *time.Duration
	if req.DefaultTtlSeconds != nil {
		if *req.DefaultTtlSeconds <= 0 {
			httpserver.WriteError(w, badRequest("defaultTtlSeconds, when set, must be greater than 0."))
			return
		}
		if *req.DefaultTtlSeconds > req.MaxTtlSeconds {
			httpserver.WriteError(w, badRequest("defaultTtlSeconds must not exceed maxTtlSeconds."))
			return
		}
		d := time.Duration(*req.DefaultTtlSeconds) * time.Second
		defaultTTL = &d
	}
	pol, err := d.Store.UpsertProjectTTLPolicy(r.Context(), store.UpsertProjectTTLPolicyParams{
		TenantID:   id.ActiveTenantID,
		ProjectID:  projectID,
		DefaultTTL: defaultTTL,
		MaxTTL:     time.Duration(req.MaxTtlSeconds) * time.Second,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, ttlPolicyToWire(pol))
}

func ttlPolicyToWire(p *store.ProjectTTLPolicy) types.TtlPolicy {
	out := types.TtlPolicy{MaxTtlSeconds: int64(p.MaxTTL.Seconds())}
	if p.DefaultTTL != nil {
		v := int64(p.DefaultTTL.Seconds())
		out.DefaultTtlSeconds = &v
	}
	return out
}
