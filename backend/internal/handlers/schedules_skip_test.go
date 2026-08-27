package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/auth"
	"github.com/timkrebs9/proxcloud/backend/internal/handlers"
	"github.com/timkrebs9/proxcloud/backend/internal/scheduler"
	"github.com/timkrebs9/proxcloud/backend/internal/store"
	"github.com/timkrebs9/proxcloud/backend/internal/store/storetest"
)

// callSkip invokes POST …/schedule/skip against a Contributor identity, mirroring
// the ResolveScope-wrapped route the real router mounts.
func callSkip(d *handlers.Deps, tenantID string) *httptest.ResponseRecorder {
	id := &auth.Identity{UserID: "u1", Email: "u@x.io", ActiveTenantID: tenantID, EffectiveRole: "contributor"}
	r := chi.NewRouter()
	r.Post("/api/tenants/{tenantId}/guests/{node}/{type}/{vmid}/schedule/skip", func(w http.ResponseWriter, req *http.Request) {
		req = req.WithContext(auth.ContextWithIdentity(req.Context(), id))
		d.SkipNextSchedule(w, req)
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/tenants/"+tenantID+"/guests/pve01/qemu/101/schedule/skip", nil)
	r.ServeHTTP(rec, req)
	return rec
}

// TestSkipNextScheduleBumpsAutoshutdownJobs covers the "skip next" handler: it
// advances every scheduled autoshutdown.* job's run_at to the NEXT cron boundary
// (leaving the schedule enabled), reports the count + the stop job's next run,
// and never touches a non-autoshutdown (e.g. ttl.*) job.
func TestSkipNextScheduleBumpsAutoshutdownJobs(t *testing.T) {
	ctx := context.Background()
	d, f, tid := scheduleHarness(t)
	pid := mustPID(t, f)

	// A daily 21:45 Europe/Berlin stop + its 21:30 warn, both currently due at
	// the next occurrence; plus a ttl.expire job that skip must leave alone.
	cron := "45 21 * * *"
	warnCron := "30 21 * * *"
	tz := "Europe/Berlin"
	base := time.Date(2026, 1, 5, 12, 0, 0, 0, time.UTC)
	stopAt, _ := scheduler.NextCron(cron, tz, base)
	warnAt, _ := scheduler.NextCron(warnCron, tz, base)

	stop := seedCronJob(t, f, "autoshutdown.stop", 101, tid, pid, cron, tz, stopAt)
	warn := seedCronJob(t, f, "autoshutdown.warn", 101, tid, pid, warnCron, tz, warnAt)
	ttlJob, _ := f.EnqueueJob(ctx, store.EnqueueJobParams{
		Kind: "one_shot", Handler: "ttl.expire", TenantID: &tid, ProjectID: &pid,
		VMID: intptrH(101), RunAt: base.Add(48 * time.Hour),
	})

	rec := callSkip(d, tid)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var got types.ScheduleSkipResult
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Skipped != 2 {
		t.Fatalf("skipped = %d, want 2 (stop + warn)", got.Skipped)
	}

	// Each autoshutdown job's run_at moved forward one occurrence.
	wantStop, _ := scheduler.NextCron(cron, tz, stopAt)
	wantWarn, _ := scheduler.NextCron(warnCron, tz, warnAt)
	if j := jobByID(f, stop.ID); j == nil || !j.RunAt.Equal(wantStop) {
		t.Fatalf("stop run_at = %v, want %v", jobRunAt(f, stop.ID), wantStop)
	}
	if j := jobByID(f, warn.ID); j == nil || !j.RunAt.Equal(wantWarn) {
		t.Fatalf("warn run_at = %v, want %v", jobRunAt(f, warn.ID), wantWarn)
	}
	// NextRunAt in the response reflects the stop job specifically.
	if got.NextRunAt == nil || !got.NextRunAt.Equal(wantStop) {
		t.Fatalf("nextRunAt = %v, want stop's %v", got.NextRunAt, wantStop)
	}
	// The ttl.* job is untouched (skip is autoshutdown-scoped).
	if j := jobByID(f, ttlJob.ID); j == nil || !j.RunAt.Equal(base.Add(48*time.Hour)) {
		t.Fatalf("ttl job run_at moved: %v", jobRunAt(f, ttlJob.ID))
	}
}

// TestSkipNextScheduleNoJobs returns an empty result (0 skipped, no next) rather
// than erroring when the guest has no scheduled autoshutdown jobs.
func TestSkipNextScheduleNoJobs(t *testing.T) {
	d, f, tid := scheduleHarness(t)
	_ = f
	rec := callSkip(d, tid)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var got types.ScheduleSkipResult
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Skipped != 0 || got.NextRunAt != nil {
		t.Fatalf("empty skip result = %+v, want {0, nil}", got)
	}
}

func seedCronJob(t *testing.T, f *storetest.Fake, handler string, vmid int, tid, pid, cron, tz string, runAt time.Time) *store.Job {
	t.Helper()
	c, z := cron, tz
	j, err := f.EnqueueJob(context.Background(), store.EnqueueJobParams{
		Kind: "recurring", Handler: handler, TenantID: &tid, ProjectID: &pid,
		VMID: intptrH(vmid), Cron: &c, Timezone: &z, RunAt: runAt, MissedPolicy: "catch_up",
	})
	if err != nil {
		t.Fatalf("seed cron job %s: %v", handler, err)
	}
	return j
}

func jobByID(f *storetest.Fake, id string) *store.Job {
	for _, j := range f.AllJobs() {
		if j.ID == id {
			c := j
			return &c
		}
	}
	return nil
}

func jobRunAt(f *storetest.Fake, id string) time.Time {
	if j := jobByID(f, id); j != nil {
		return j.RunAt
	}
	return time.Time{}
}

func intptrH(v int) *int { return &v }
