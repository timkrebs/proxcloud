package handlers

import (
	"net/http"
	"sort"
	"strconv"
	"time"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/httpserver"
	"github.com/timkrebs9/proxcloud/backend/internal/store"
)

const (
	activityDefaultLimit = 50
	activityMaxLimit     = 200
)

// GetActivity serves GET /api/tenants/{tenantId}/activity (Reader): the merged
// activity timeline. The audit_log is the paginated spine (tenant-filtered,
// keyset by ts DESC); the PVE task feed is a live overlay for tasks targeting the
// tenant's owned VMIDs, included only within the page's time window. Both are
// normalized to ActivityEntry, merged, sorted ts DESC, and truncated to limit.
// Task history PVE has rotated out simply ages off the overlay — never fabricated.
func (d *Deps) GetActivity(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIdentity(w, r)
	if !ok || !d.requireStore(w) {
		return
	}
	tenantID := id.ActiveTenantID

	q := r.URL.Query()
	limit := activityDefaultLimit
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > activityMaxLimit {
		limit = activityMaxLimit
	}
	source := q.Get("source") // "" | "audit" | "task"
	projectFilter := q.Get("projectId")
	outcomeFilter := q.Get("outcome")

	var before *time.Time
	if v := q.Get("before"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			httpserver.WriteError(w, badRequest("before must be an RFC3339 timestamp."))
			return
		}
		before = &t
	}

	// --- spine: audit_log (tenant-filtered + keyset in SQL) ---
	spine, err := d.Store.ListAudit(r.Context(), store.AuditQuery{
		TenantID: tenantID, Before: before, Limit: limit,
		ProjectID: projectFilter, Outcome: outcomeFilter,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}

	// Enrichment maps: actor display names (batch) + project names.
	actorIDs := map[string]struct{}{}
	for _, e := range spine {
		if e.ActorUserID != nil && *e.ActorUserID != "" {
			actorIDs[*e.ActorUserID] = struct{}{}
		}
	}
	actors := d.resolveActors(r, actorIDs)
	projName := d.projectNames(r, tenantID)

	entries := make([]types.ActivityEntry, 0, len(spine)+limit)
	for _, e := range spine {
		entries = append(entries, auditToActivity(e, actors, projName))
	}

	// NextBefore: the oldest audit row's ts when the spine filled the page (more
	// remain), else null. Also bounds the task overlay's lower edge.
	var nextBefore *time.Time
	moreAudit := len(spine) == limit
	if moreAudit && len(spine) > 0 {
		ts := spine[len(spine)-1].TS
		nextBefore = &ts
	}

	// --- overlay: PVE tasks for the tenant's owned VMIDs, within the window ---
	if source != "audit" {
		taskEntries, err := d.taskOverlay(r, tenantID, before, nextBefore, projName, projectFilter, outcomeFilter)
		if err != nil {
			httpserver.WriteError(w, err)
			return
		}
		entries = append(entries, taskEntries...)
	}

	// Apply the source filter (audit already skips the overlay above).
	if source == "task" {
		filtered := entries[:0]
		for _, e := range entries {
			if e.Source == "task" {
				filtered = append(filtered, e)
			}
		}
		entries = filtered
	}

	sort.SliceStable(entries, func(i, j int) bool {
		if !entries[i].TS.Equal(entries[j].TS) {
			return entries[i].TS.After(entries[j].TS)
		}
		return entries[i].ID > entries[j].ID
	})
	if len(entries) > limit {
		entries = entries[:limit]
	}

	httpserver.WriteJSON(w, http.StatusOK, types.ActivityPage{Entries: entries, NextBefore: nextBefore})
}

// taskOverlay builds the task-feed ActivityEntries for tasks targeting the
// tenant's owned VMIDs whose start time falls in the window [lowerBound, before).
// lowerBound is nextBefore (the oldest audit ts) when more audit rows remain,
// else the zero time (last page → sweep in all older tasks).
func (d *Deps) taskOverlay(r *http.Request, tenantID string, before, lowerBound *time.Time, projName map[string]string, projectFilter, outcomeFilter string) ([]types.ActivityEntry, error) {
	owns, err := d.Store.ListOwnershipByTenant(r.Context(), tenantID)
	if err != nil {
		return nil, err
	}
	ownByVMID := make(map[int]store.ResourceOwnership, len(owns))
	for _, o := range owns {
		if o.Status == "active" || o.Status == "pending" {
			ownByVMID[o.VMID] = o
		}
	}

	infos, err := d.PVE.ClusterTasks(r.Context())
	if err != nil {
		return nil, err
	}

	out := []types.ActivityEntry{}
	for _, t := range infos {
		s := d.taskSummary(t)
		if s.Resource == nil {
			continue
		}
		own, owned := ownByVMID[s.Resource.VMID]
		if !owned {
			continue
		}
		if before != nil && !s.StartedAt.Before(*before) {
			continue // newer than the page cursor
		}
		if lowerBound != nil && s.StartedAt.Before(*lowerBound) {
			continue // belongs to an older page
		}
		if projectFilter != "" && own.ProjectID != projectFilter {
			continue
		}
		if outcomeFilter != "" && s.Status != outcomeFilter {
			continue
		}
		out = append(out, types.ActivityEntry{
			ID:          "task:" + s.UPID,
			Source:      "task",
			TS:          s.StartedAt,
			Actor:       t.User,
			Action:      s.Action,
			TargetType:  "guest",
			TargetID:    strconv.Itoa(s.Resource.VMID),
			Outcome:     s.Status, // running | succeeded | failed
			ProjectID:   own.ProjectID,
			ProjectName: projName[own.ProjectID],
			UPID:        s.UPID,
		})
	}
	return out, nil
}

// auditToActivity normalizes one audit row into the wire ActivityEntry, resolving
// the actor's display name (nil actor → "system", e.g. the reconciler) and the
// project name.
func auditToActivity(e store.AuditEntry, actors map[string]store.User, projName map[string]string) types.ActivityEntry {
	actor := "system"
	if e.ActorUserID != nil && *e.ActorUserID != "" {
		if u, ok := actors[*e.ActorUserID]; ok {
			if u.DisplayName != "" {
				actor = u.DisplayName
			} else {
				actor = u.Email
			}
		} else {
			actor = ""
		}
	}
	entry := types.ActivityEntry{
		ID:      e.ID,
		Source:  "audit",
		TS:      e.TS,
		Actor:   actor,
		Action:  e.Action,
		Outcome: e.Outcome,
		Detail:  e.Detail,
	}
	if e.TargetType != nil {
		entry.TargetType = *e.TargetType
	}
	if e.TargetID != nil {
		entry.TargetID = *e.TargetID
	}
	if e.ProjectID != nil {
		entry.ProjectID = *e.ProjectID
		entry.ProjectName = projName[*e.ProjectID]
	}
	return entry
}

// resolveActors batches user-id → row resolution (no N+1); enrichment is
// best-effort (a failure leaves actor names blank, never blocks the timeline).
func (d *Deps) resolveActors(r *http.Request, ids map[string]struct{}) map[string]store.User {
	if len(ids) == 0 {
		return map[string]store.User{}
	}
	list := make([]string, 0, len(ids))
	for id := range ids {
		list = append(list, id)
	}
	users, err := d.Store.ListUsersByIDs(r.Context(), list)
	if err != nil {
		d.logger().Warn("activity: actor enrichment failed", "err", err)
		return map[string]store.User{}
	}
	return users
}

// projectNames builds the tenant's projectID → display-name map (best-effort).
func (d *Deps) projectNames(r *http.Request, tenantID string) map[string]string {
	out := map[string]string{}
	projs, err := d.Store.ListProjectsByTenant(r.Context(), tenantID)
	if err != nil {
		d.logger().Warn("activity: project-name enrichment failed", "tenant", tenantID, "err", err)
		return out
	}
	for _, p := range projs {
		out[p.ID] = p.Name
	}
	return out
}
