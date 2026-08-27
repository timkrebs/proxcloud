package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/httpserver"
	"github.com/timkrebs9/proxcloud/backend/internal/scheduler"
	"github.com/timkrebs9/proxcloud/backend/internal/store"
)

// scheduleToWire converts a stored schedule into its wire form.
func scheduleToWire(s *store.Schedule) types.Schedule {
	days := s.DaysOfWeek
	if days == nil {
		days = []int{}
	}
	return types.Schedule{
		ID:            s.ID,
		Scope:         s.Scope,
		TenantID:      s.TenantID,
		ProjectID:     s.ProjectID,
		VMID:          s.VMID,
		ShutdownTime:  s.ShutdownTime,
		AutoStartTime: s.AutoStartTime,
		DaysOfWeek:    days,
		Timezone:      s.Timezone,
		GraceSeconds:  s.GraceSeconds,
		Enabled:       s.Enabled,
		OptOut:        s.OptOut,
		CreatedAt:     s.CreatedAt,
		UpdatedAt:     s.UpdatedAt,
	}
}

// validHHMM reports whether s is a well-formed "HH:MM" 24h clock string.
func validHHMM(s string) bool {
	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return false
	}
	hh, err := strconv.Atoi(parts[0])
	if err != nil || hh < 0 || hh > 23 {
		return false
	}
	mm, err := strconv.Atoi(parts[1])
	return err == nil && mm >= 0 && mm <= 59
}

// decodeSchedule decodes and validates a ScheduleRequest: HH:MM times, a
// non-empty 0..6 day list, a real IANA timezone, and a positive grace. Cron is
// never accepted from the client — it is derived internally (ADR-0019).
func decodeSchedule(r *http.Request) (types.ScheduleRequest, *types.APIError) {
	var req types.ScheduleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return req, badRequest("Request body must be a JSON ScheduleRequest.")
	}
	if !validHHMM(req.ShutdownTime) {
		return req, badRequest("shutdownTime must be HH:MM (24-hour).")
	}
	if req.AutoStartTime != nil && *req.AutoStartTime != "" && !validHHMM(*req.AutoStartTime) {
		return req, badRequest("autoStartTime must be HH:MM (24-hour).")
	}
	if len(req.DaysOfWeek) == 0 {
		return req, badRequest("daysOfWeek must list at least one day (0-6, Sun-Sat).")
	}
	for _, d := range req.DaysOfWeek {
		if d < 0 || d > 6 {
			return req, badRequest("daysOfWeek values must be 0-6 (Sun-Sat).")
		}
	}
	if _, err := time.LoadLocation(req.Timezone); err != nil {
		return req, badRequest("timezone must be a valid IANA name (e.g. Europe/Berlin).")
	}
	if req.GraceSeconds <= 0 || req.GraceSeconds > maxGraceSeconds {
		return req, badRequest("graceSeconds must be between 1 and 300.")
	}
	return req, nil
}

// maxGraceSeconds caps a schedule's force-stop grace window. It must stay well
// under the scheduler's per-handler timeout: GuestShutdown delegates the whole
// graceful→force window to PVE over `timeout=graceSec`, and the stop handler
// waits grace + margin for it to settle, so an unbounded grace could outrun the
// handler timeout and be misread as a failure.
const maxGraceSeconds = 300

// GetResourceSchedule serves GET …/guests/{node}/{type}/{vmid}/schedule (Reader):
// the guest's own resource-scope schedule, or 404 when none is set.
func (d *Deps) GetResourceSchedule(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireIdentity(w, r); !ok || !d.requireStore(w) {
		return
	}
	ref, apiErr := guestRef(r)
	if apiErr != nil {
		httpserver.WriteError(w, apiErr)
		return
	}
	sc, err := d.Store.GetResourceSchedule(r.Context(), ref.VMID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpserver.WriteError(w, notFound("No schedule set for this guest."))
			return
		}
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, scheduleToWire(sc))
}

// PutResourceSchedule serves PUT …/guests/{node}/{type}/{vmid}/schedule
// (Contributor): create or replace the guest's schedule, then re-materialize its
// jobs (when the feature is active).
func (d *Deps) PutResourceSchedule(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIdentity(w, r)
	if !ok || !d.requireStore(w) {
		return
	}
	ref, apiErr := guestRef(r)
	if apiErr != nil {
		httpserver.WriteError(w, apiErr)
		return
	}
	req, apiErr := decodeSchedule(r)
	if apiErr != nil {
		httpserver.WriteError(w, apiErr)
		return
	}
	// Ownership is already tenant-verified by ResolveScope; read it for the guest's
	// project (the schedule's project_id) — a missing row is a 404, never invented.
	own, err := d.Store.GetOwnershipByVMID(r.Context(), ref.VMID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpserver.WriteError(w, notFound("Guest not found."))
			return
		}
		httpserver.WriteError(w, err)
		return
	}
	userID := id.UserID
	params := store.UpsertResourceScheduleParams{
		TenantID:      own.TenantID,
		ProjectID:     own.ProjectID,
		VMID:          ref.VMID,
		ShutdownTime:  req.ShutdownTime,
		AutoStartTime: req.AutoStartTime,
		DaysOfWeek:    req.DaysOfWeek,
		Timezone:      req.Timezone,
		GraceSeconds:  req.GraceSeconds,
		Enabled:       req.Enabled,
		OptOut:        req.OptOut,
		CreatedBy:     &userID,
	}
	// Persist the schedule and re-materialize its jobs in ONE transaction, so a
	// materialize failure rolls back the upsert too — no "saved but no jobs" state.
	var sc *store.Schedule
	err = d.Store.WithTx(r.Context(), func(txs store.Store) error {
		var e error
		if sc, e = txs.UpsertResourceSchedule(r.Context(), params); e != nil {
			return e
		}
		if d.AutoShutdownEnabled && d.AutoShutdown != nil {
			return d.AutoShutdown.MaterializeForGuestWith(r.Context(), txs, *own)
		}
		return nil
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, scheduleToWire(sc))
}

// DeleteResourceSchedule serves DELETE …/guests/{node}/{type}/{vmid}/schedule
// (Contributor): remove the guest's schedule and re-materialize (so it falls back
// to any project schedule, or ends up with no jobs).
func (d *Deps) DeleteResourceSchedule(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireIdentity(w, r); !ok || !d.requireStore(w) {
		return
	}
	ref, apiErr := guestRef(r)
	if apiErr != nil {
		httpserver.WriteError(w, apiErr)
		return
	}
	// Delete the schedule and re-resolve the guest's jobs atomically: dropping a
	// resource schedule may re-expose a project schedule, so MaterializeForGuest
	// cancels the old jobs and emits the newly-effective set (or none). Without the
	// feature active, cancel only this guest's auto-shutdown jobs (prefix-scoped —
	// a broad cancel would drop the guest's TTL jobs if TTL is enabled while
	// auto-shutdown is not).
	err := d.Store.WithTx(r.Context(), func(txs store.Store) error {
		if e := txs.DeleteResourceSchedule(r.Context(), ref.VMID); e != nil {
			return e
		}
		if d.AutoShutdownEnabled && d.AutoShutdown != nil {
			own, e := txs.GetOwnershipByVMID(r.Context(), ref.VMID)
			if errors.Is(e, store.ErrNotFound) {
				return nil // guest gone out of band; nothing to re-materialize
			}
			if e != nil {
				return e
			}
			return d.AutoShutdown.MaterializeForGuestWith(r.Context(), txs, *own)
		}
		_, e := txs.CancelJobsForVMIDByPrefix(r.Context(), ref.VMID, "autoshutdown.")
		return e
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpserver.WriteError(w, notFound("No schedule set for this guest."))
			return
		}
		httpserver.WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// SkipNextSchedule serves POST …/guests/{node}/{type}/{vmid}/schedule/skip
// (Contributor): advance the guest's next auto-shutdown occurrence (stop/warn/
// start) by one cron boundary without disabling the schedule (ADR-0019). Skip is
// implemented by rescheduling each scheduled autoshutdown.* job's run_at to the
// occurrence AFTER its current one — a per-occurrence skip on the durable jobs.
func (d *Deps) SkipNextSchedule(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireIdentity(w, r); !ok || !d.requireStore(w) {
		return
	}
	ref, apiErr := guestRef(r)
	if apiErr != nil {
		httpserver.WriteError(w, apiErr)
		return
	}
	vmid := ref.VMID
	jobs, err := d.Store.ListJobs(r.Context(), store.JobFilter{VMID: &vmid, Status: "scheduled"})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	result := types.ScheduleSkipResult{}
	for i := range jobs {
		j := jobs[i]
		if !strings.HasPrefix(j.Handler, "autoshutdown.") || j.Cron == nil || *j.Cron == "" {
			continue
		}
		tz := ""
		if j.Timezone != nil {
			tz = *j.Timezone
		}
		next, err := scheduler.NextCron(*j.Cron, tz, j.RunAt)
		if err != nil {
			d.logger().Warn("skip schedule: next cron", "job", j.ID, "err", err)
			continue
		}
		if err := d.Store.BumpScheduledRunAt(r.Context(), j.ID, next); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				continue // raced to running/terminal — nothing to skip
			}
			d.logger().Warn("skip schedule: bump run_at", "job", j.ID, "err", err)
			continue
		}
		result.Skipped++
		if j.Handler == "autoshutdown.stop" {
			nextCopy := next
			result.NextRunAt = &nextCopy
		}
	}
	httpserver.WriteJSON(w, http.StatusOK, result)
}

// GetProjectSchedule serves GET …/projects/{projectId}/schedule (Reader).
func (d *Deps) GetProjectSchedule(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIdentity(w, r)
	if !ok || !d.requireStore(w) {
		return
	}
	projectID := chi.URLParam(r, "projectId")
	sc, err := d.Store.GetProjectSchedule(r.Context(), id.ActiveTenantID, projectID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpserver.WriteError(w, notFound("No schedule set for this project."))
			return
		}
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, scheduleToWire(sc))
}

// PutProjectSchedule serves PUT …/projects/{projectId}/schedule (Contributor):
// create or replace the project schedule and re-materialize every guest in the
// project (honoring per-guest overrides/opt-outs).
func (d *Deps) PutProjectSchedule(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIdentity(w, r)
	if !ok || !d.requireStore(w) {
		return
	}
	projectID := chi.URLParam(r, "projectId")
	req, apiErr := decodeSchedule(r)
	if apiErr != nil {
		httpserver.WriteError(w, apiErr)
		return
	}
	userID := id.UserID
	sc, err := d.Store.UpsertProjectSchedule(r.Context(), store.UpsertProjectScheduleParams{
		TenantID:      id.ActiveTenantID,
		ProjectID:     projectID,
		ShutdownTime:  req.ShutdownTime,
		AutoStartTime: req.AutoStartTime,
		DaysOfWeek:    req.DaysOfWeek,
		Timezone:      req.Timezone,
		GraceSeconds:  req.GraceSeconds,
		Enabled:       req.Enabled,
		CreatedBy:     &userID,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	if d.AutoShutdownEnabled && d.AutoShutdown != nil {
		if err := d.AutoShutdown.MaterializeProject(r.Context(), projectID); err != nil {
			httpserver.WriteError(w, err)
			return
		}
	}
	httpserver.WriteJSON(w, http.StatusOK, scheduleToWire(sc))
}

// DeleteProjectSchedule serves DELETE …/projects/{projectId}/schedule
// (Contributor): remove the project schedule and re-materialize every guest (those
// with their own resource schedule keep it; the rest lose the inherited jobs).
func (d *Deps) DeleteProjectSchedule(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIdentity(w, r)
	if !ok || !d.requireStore(w) {
		return
	}
	projectID := chi.URLParam(r, "projectId")
	if err := d.Store.DeleteProjectSchedule(r.Context(), id.ActiveTenantID, projectID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpserver.WriteError(w, notFound("No schedule set for this project."))
			return
		}
		httpserver.WriteError(w, err)
		return
	}
	if d.AutoShutdownEnabled && d.AutoShutdown != nil {
		if err := d.AutoShutdown.MaterializeProject(r.Context(), projectID); err != nil {
			httpserver.WriteError(w, err)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}
