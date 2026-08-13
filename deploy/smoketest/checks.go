package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// Result is one row of the smoke pass/fail table.
type Result struct {
	Name   string
	Pass   bool
	Detail string
}

// Runner holds the client, config, resolved ids, and accumulated results.
type Runner struct {
	cfg Config
	api *apiClient

	tenantID  string
	projectID string

	guestName string
	created   bool // create returned 202 (a delete is now owed)
	deleted   bool // delete polled to completion

	results []Result
}

func newRunner(cfg Config) (*Runner, error) {
	api, err := newAPIClient(cfg.BaseURL, cfg.HTTPTimeout)
	if err != nil {
		return nil, err
	}
	return &Runner{cfg: cfg, api: api, guestName: guestName(cfg.VMID)}, nil
}

func (r *Runner) pass(name, detail string) { r.results = append(r.results, Result{name, true, detail}) }
func (r *Runner) fail(name, detail string) {
	r.results = append(r.results, Result{name, false, detail})
}

// run executes the assertions fail-fast, but ALWAYS runs cleanup afterwards so a
// created throwaway LXC is never left behind. It returns true iff every
// recorded result passed.
func (r *Runner) run(ctx context.Context) bool {
	// Ordered assertions; the first failure short-circuits the chain (fail-fast).
	steps := []func(context.Context) bool{
		r.checkVersion,
		r.checkLogin,
		r.resolveTenant,
		r.checkListResources,
		r.resolveProject,
		r.checkCreateGuest,
		r.checkSSE,
		r.checkDeleteGuest,
	}
	ok := true
	for _, step := range steps {
		if !step(ctx) {
			ok = false
			break
		}
	}

	// Deferred cleanup: if we created a guest but never confirmed its delete
	// (a later assertion failed), tear it down best-effort so the reserved VMID
	// is free for the next run.
	if r.created && !r.deleted {
		r.cleanup(ctx)
	}

	for _, res := range r.results {
		if !res.Pass {
			return false
		}
	}
	return ok
}

// 1. version — the deployed build is the one answering.
func (r *Runner) checkVersion(ctx context.Context) bool {
	const name = "version"
	var v versionInfo
	if err := r.api.getJSON(ctx, "/api/v1/version", &v); err != nil {
		r.fail(name, err.Error())
		return false
	}
	if r.cfg.ExpectRef == "" {
		if v.Commit == "" {
			r.fail(name, "version returned an empty commit")
			return false
		}
		r.pass(name, fmt.Sprintf("commit=%s semver=%s (no expected ref pinned)", short(v.Commit), v.Semver))
		return true
	}
	field, want := versionField(r.cfg.ExpectRef)
	got := v.Commit
	if field == "semver" {
		got = v.Semver
	}
	if got != want {
		r.fail(name, fmt.Sprintf("deployed .%s=%q, expected %q (commit=%s semver=%s)", field, got, want, short(v.Commit), v.Semver))
		return false
	}
	r.pass(name, fmt.Sprintf(".%s matches %s", field, want))
	return true
}

// 2. login — session cookie via the real auth path (no interactive TOTP).
func (r *Runner) checkLogin(ctx context.Context) bool {
	const name = "login"
	code, body, err := r.api.do(ctx, http.MethodPost, "/api/auth/login", loginRequest{Email: r.cfg.Email, Password: r.cfg.Password})
	if err != nil {
		r.fail(name, err.Error())
		return false
	}
	if code != http.StatusOK {
		r.fail(name, httpErr(http.MethodPost, "/api/auth/login", code, body).Error())
		return false
	}
	var lr loginResponse
	if err := jsonUnmarshal(body, &lr); err != nil {
		r.fail(name, err.Error())
		return false
	}
	if lr.TotpRequired {
		r.fail(name, "smoke user has TOTP enabled — the seed must create a non-TOTP user (ADR-0016 §3)")
		return false
	}
	if !r.hasSessionCookie() {
		r.fail(name, "login returned 200 but set no proxcloud_session cookie")
		return false
	}
	r.pass(name, fmt.Sprintf("session established for %s", r.cfg.Email))
	return true
}

// hasSessionCookie confirms the jar captured the session cookie for the base host.
func (r *Runner) hasSessionCookie() bool {
	u, err := baseURL(r.cfg.BaseURL)
	if err != nil {
		return false
	}
	for _, ck := range r.api.hc.Jar.Cookies(u) {
		if ck.Name == "proxcloud_session" && ck.Value != "" {
			return true
		}
	}
	return false
}

// resolveTenant maps the tenant slug/id to a concrete tenant id via /api/auth/me,
// confirming the smoke user actually reaches the tenant. Falls back to using the
// ref verbatim (assumed an id) if /me does not list it.
func (r *Runner) resolveTenant(ctx context.Context) bool {
	const name = "resolve-tenant"
	var me meResponse
	if err := r.api.getJSON(ctx, "/api/auth/me", &me); err != nil {
		r.fail(name, err.Error())
		return false
	}
	for _, t := range me.Tenants {
		if t.ID == r.cfg.TenantRef || (t.Slug != "" && t.Slug == r.cfg.TenantRef) {
			r.tenantID = t.ID
			r.pass(name, fmt.Sprintf("tenant %q -> id %s (role %s)", r.cfg.TenantRef, t.ID, emptyDash(t.Role)))
			return true
		}
	}
	// Not listed by /me: assume the ref is already a tenant id. A wrong id will
	// 404 on the tenant-scoped calls below (honest failure, no existence leak).
	r.tenantID = r.cfg.TenantRef
	r.pass(name, fmt.Sprintf("tenant %q not in /me memberships (%d listed) — using it as a raw id", r.cfg.TenantRef, len(me.Tenants)))
	return true
}

// 3. list resources — tenant-scoped read + authz (may be empty).
func (r *Runner) checkListResources(ctx context.Context) bool {
	const name = "list-resources"
	var guests []guestSummary
	path := r.tenantPath("/resources")
	if err := r.api.getJSON(ctx, path, &guests); err != nil {
		r.fail(name, err.Error())
		return false
	}
	r.pass(name, fmt.Sprintf("%d resource(s) in the smoke tenant", len(guests)))
	return true
}

// resolveProject maps the project slug/id to a concrete project id via the
// tenant's projects list. Falls back to using the ref verbatim (assumed an id).
func (r *Runner) resolveProject(ctx context.Context) bool {
	const name = "resolve-project"
	var projects []project
	if err := r.api.getJSON(ctx, r.tenantPath("/projects"), &projects); err != nil {
		r.fail(name, err.Error())
		return false
	}
	for _, p := range projects {
		if p.ID == r.cfg.ProjectRef || (p.Slug != "" && p.Slug == r.cfg.ProjectRef) {
			r.projectID = p.ID
			r.pass(name, fmt.Sprintf("project %q -> id %s", r.cfg.ProjectRef, p.ID))
			return true
		}
	}
	r.projectID = r.cfg.ProjectRef
	r.pass(name, fmt.Sprintf("project %q not found in %d listed — using it as a raw id", r.cfg.ProjectRef, len(projects)))
	return true
}

// 4a. create — the real async path: POST → deploymentId → poll to succeeded.
// A stale guest at the reserved VMID (from an aborted run) is deleted first so
// the create is idempotent-ish.
func (r *Runner) checkCreateGuest(ctx context.Context) bool {
	const name = "create-lxc"
	if !nameRe.MatchString(r.guestName) {
		r.fail(name, fmt.Sprintf("derived guest name %q is invalid", r.guestName))
		return false
	}
	r.preCleanStaleVMID(ctx)

	req := createGuestRequest{
		Type:             "lxc",
		Name:             r.guestName,
		Node:             r.cfg.Node,
		VMID:             r.cfg.VMID,
		ProjectID:        r.projectID,
		Source:           createSource{Mode: "vztmpl", VztmplVolID: r.cfg.Template},
		Cores:            1,
		MemoryMB:         128,
		DiskGB:           1,
		Storage:          r.cfg.Storage,
		Bridge:           r.cfg.Bridge,
		StartAfterCreate: false, // stopped => deletable
	}
	// A deleted VMID's ownership is released asynchronously (once the destroy
	// task completes), so a create right after preCleanStaleVMID — or after a
	// prior run's own delete — can briefly see a `conflict` 409 before the
	// tombstone lands. Retry through that window; any other error fails at once.
	code, body, err := r.createWithConflictRetry(ctx, req, 30*time.Second)
	if err != nil {
		r.fail(name, err.Error())
		return false
	}
	if code != http.StatusAccepted {
		r.fail(name, httpErr(http.MethodPost, r.tenantPath("/guests"), code, body).Error())
		return false
	}
	var cr createGuestResponse
	if err := jsonUnmarshal(body, &cr); err != nil {
		r.fail(name, err.Error())
		return false
	}
	r.created = true // a delete is now owed even if the poll fails

	dep, err := r.pollDeployment(ctx, cr.DeploymentID)
	if err != nil {
		r.fail(name, err.Error())
		return false
	}
	if dep.Status != "succeeded" {
		r.fail(name, fmt.Sprintf("deployment %s ended %q (expected succeeded)", cr.DeploymentID, dep.Status))
		return false
	}
	r.pass(name, fmt.Sprintf("LXC %d (%s) created via deployment %s", cr.VMID, r.guestName, short(cr.DeploymentID)))
	return true
}

// createWithConflictRetry POSTs the create, retrying ONLY while the response is
// a `conflict` 409 — the reserved VMID's ownership row has not been released yet
// (the backend tombstones it asynchronously after a destroy completes) — up to
// the budget. quota_exceeded and every other status return immediately.
func (r *Runner) createWithConflictRetry(ctx context.Context, req createGuestRequest, budget time.Duration) (int, []byte, error) {
	deadline := time.Now().Add(budget)
	for {
		code, body, err := r.api.do(ctx, http.MethodPost, r.tenantPath("/guests"), req)
		if err != nil {
			return 0, nil, err
		}
		if code != http.StatusConflict || errorCode(body) != "conflict" || time.Now().After(deadline) {
			return code, body, nil
		}
		time.Sleep(2 * time.Second)
	}
}

// 5. sse — the stream flushes through the proxy (ADR-0015 §5). Any SSE frame
// (the immediate retry: preamble, a heartbeat, or a real owned event) within
// the timeout satisfies "≥1 SSE frame"; a real event: frame is noted.
func (r *Runner) checkSSE(ctx context.Context) bool {
	const name = "sse"
	sctx, cancel := context.WithTimeout(ctx, r.cfg.SSETimeout)
	defer cancel()
	res, err := r.api.readSSE(sctx, "/api/events")
	if err != nil {
		r.fail(name, err.Error())
		return false
	}
	detail := fmt.Sprintf("%d frame(s); first=%q", res.Frames, res.First)
	if res.SawEvent {
		detail += "; delivered a real event frame"
	}
	r.pass(name, detail)
	return true
}

// 4b. delete — DELETE → poll the task to succeeded (guest gone).
func (r *Runner) checkDeleteGuest(ctx context.Context) bool {
	const name = "delete-lxc"
	upid, err := r.deleteGuest(ctx)
	if err != nil {
		r.fail(name, err.Error())
		return false
	}
	ts, err := r.pollTask(ctx, upid)
	if err != nil {
		r.fail(name, err.Error())
		return false
	}
	if ts.Status != "succeeded" {
		r.fail(name, fmt.Sprintf("delete task ended %q (%s)", ts.Status, ts.ExitStatus))
		return false
	}
	r.deleted = true
	r.pass(name, fmt.Sprintf("LXC %d deleted (task %s)", r.cfg.VMID, short(upid)))
	return true
}

// deleteGuest issues the confirmed, purging delete and returns the task UPID.
func (r *Runner) deleteGuest(ctx context.Context) (string, error) {
	path := fmt.Sprintf("%s/guests/%s/lxc/%d?purge=1", r.tenantBase(), r.cfg.Node, r.cfg.VMID)
	code, body, err := r.api.do(ctx, http.MethodDelete, path, deleteRequest{ConfirmName: r.guestName})
	if err != nil {
		return "", err
	}
	if code != http.StatusAccepted {
		return "", httpErr(http.MethodDelete, path, code, body)
	}
	var tr taskRef
	if err := jsonUnmarshal(body, &tr); err != nil {
		return "", err
	}
	if tr.UPID == "" {
		return "", fmt.Errorf("delete returned 202 with an empty UPID")
	}
	return tr.UPID, nil
}

// preCleanStaleVMID best-effort deletes any existing guest occupying the
// reserved VMID so a create after an aborted run does not 409.
func (r *Runner) preCleanStaleVMID(ctx context.Context) {
	var guests []guestSummary
	if err := r.api.getJSON(ctx, r.tenantPath("/resources"), &guests); err != nil {
		return
	}
	for _, g := range guests {
		if g.VMID != r.cfg.VMID {
			continue
		}
		path := fmt.Sprintf("%s/guests/%s/%s/%d?purge=1", r.tenantBase(), g.Node, g.Type, g.VMID)
		code, body, err := r.api.do(ctx, http.MethodDelete, path, deleteRequest{ConfirmName: g.Name})
		if err != nil || code != http.StatusAccepted {
			return // best-effort; the create will surface a real conflict if it persists
		}
		var tr taskRef
		if jsonUnmarshal(body, &tr) == nil && tr.UPID != "" {
			_, _ = r.pollTask(ctx, tr.UPID)
		}
		return
	}
}

// cleanup is the deferred teardown when an assertion failed after create.
func (r *Runner) cleanup(ctx context.Context) {
	cctx, cancel := context.WithTimeout(ctx, r.cfg.TaskTimeout)
	defer cancel()
	upid, err := r.deleteGuest(cctx)
	if err != nil {
		r.results = append(r.results, Result{"cleanup", false, "could not delete throwaway LXC: " + err.Error()})
		return
	}
	ts, err := r.pollTask(cctx, upid)
	if err != nil || ts.Status != "succeeded" {
		r.results = append(r.results, Result{"cleanup", false, fmt.Sprintf("throwaway LXC delete did not complete (task %s)", short(upid))})
		return
	}
	r.deleted = true
	r.results = append(r.results, Result{"cleanup", true, "throwaway LXC removed after a failed run"})
}

// pollDeployment polls the deployment until it leaves "running" or the task
// timeout fires.
func (r *Runner) pollDeployment(ctx context.Context, id string) (deployment, error) {
	deadline := time.Now().Add(r.cfg.TaskTimeout)
	for {
		var dep deployment
		if err := r.api.getJSON(ctx, r.tenantPath("/deployments/"+id), &dep); err != nil {
			return deployment{}, fmt.Errorf("poll deployment %s: %w", short(id), err)
		}
		if dep.Status != "running" && dep.Status != "" {
			return dep, nil
		}
		if time.Now().After(deadline) {
			return deployment{}, fmt.Errorf("deployment %s still running after %s", short(id), r.cfg.TaskTimeout)
		}
		time.Sleep(3 * time.Second)
	}
}

// pollTask polls a Proxmox task via the tenant-scoped task endpoint until it
// leaves "running" or the task timeout fires.
func (r *Runner) pollTask(ctx context.Context, upid string) (taskSummary, error) {
	deadline := time.Now().Add(r.cfg.TaskTimeout)
	for {
		var ts taskSummary
		if err := r.api.getJSON(ctx, r.tenantPath("/tasks/"+urlEscape(upid)), &ts); err != nil {
			return taskSummary{}, fmt.Errorf("poll task %s: %w", short(upid), err)
		}
		if ts.Status != "running" && ts.Status != "" {
			return ts, nil
		}
		if time.Now().After(deadline) {
			return taskSummary{}, fmt.Errorf("task %s still running after %s", short(upid), r.cfg.TaskTimeout)
		}
		time.Sleep(3 * time.Second)
	}
}

func (r *Runner) tenantBase() string           { return "/api/tenants/" + r.tenantID }
func (r *Runner) tenantPath(sub string) string { return r.tenantBase() + sub }

// report prints the pass/fail table to stdout and, when GITHUB_STEP_SUMMARY is
// set, appends a markdown table to the job summary (ADR-0014 §6).
func (r *Runner) report() {
	allPass := true
	for _, res := range r.results {
		if !res.Pass {
			allPass = false
		}
	}

	fmt.Printf("\nSmoke results (%s)\n", r.cfg.BaseURL)
	for _, res := range r.results {
		mark := "PASS"
		if !res.Pass {
			mark = "FAIL"
		}
		fmt.Printf("  [%s] %-16s %s\n", mark, res.Name, res.Detail)
	}
	if allPass {
		fmt.Printf("SMOKE: PASS (%d assertions)\n", len(r.results))
	} else {
		fmt.Printf("SMOKE: FAIL\n")
	}

	if r.cfg.SummaryFile != "" {
		r.writeSummary(allPass)
	}
}

func (r *Runner) writeSummary(allPass bool) {
	f, err := os.OpenFile(r.cfg.SummaryFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	var b strings.Builder
	status := "PASS"
	if !allPass {
		status = "FAIL"
	}
	fmt.Fprintf(&b, "### Smoke — %s — **%s**\n\n", r.cfg.BaseURL, status)
	b.WriteString("| Assertion | Result | Detail |\n|---|---|---|\n")
	for _, res := range r.results {
		mark := "✅ pass"
		if !res.Pass {
			mark = "❌ fail"
		}
		fmt.Fprintf(&b, "| `%s` | %s | %s |\n", res.Name, mark, mdEscape(res.Detail))
	}
	b.WriteString("\n")
	_, _ = f.WriteString(b.String())
}
