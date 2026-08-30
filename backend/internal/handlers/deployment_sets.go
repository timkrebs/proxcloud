package handlers

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/authz"
	"github.com/timkrebs9/proxcloud/backend/internal/bootstrap"
	"github.com/timkrebs9/proxcloud/backend/internal/catalog"
	"github.com/timkrebs9/proxcloud/backend/internal/deploy"
	"github.com/timkrebs9/proxcloud/backend/internal/httpserver"
	"github.com/timkrebs9/proxcloud/backend/internal/proxmox"
	"github.com/timkrebs9/proxcloud/backend/internal/store"
)

// snippetRemoveTimeout bounds one best-effort snippet removal on set teardown.
const snippetRemoveTimeout = 60 * time.Second

// setsReady guards the deployment-set routes: with the feature off (the default)
// or the catalog not loaded, the endpoints report "not enabled" as a 404 (no
// capability leak). Mirrors catalogReady — the routes are always mounted so the
// permission/audit completeness tests stay green; behavior is gated here. A set is
// a catalog action (it renders per-role cloud-init and rides the snippet writer),
// so a loaded catalog is required even when the flag is on.
func (d *Deps) setsReady(w http.ResponseWriter) bool {
	if !d.DeploymentSetsEnabled || d.Catalog == nil {
		httpserver.WriteError(w, notFound("Deployment sets are not enabled."))
		return false
	}
	return true
}

// ListSets serves GET /api/tenants/{tenantId}/deployment-sets (Reader): the
// tenant's deployment sets with each member reconstructed from its ownership row
// (the durable truth — no dependency on the in-memory engine).
func (d *Deps) ListSets(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIdentity(w, r)
	if !ok || !d.requireStore(w) {
		return
	}
	if !d.setsReady(w) {
		return
	}
	sets, err := d.Store.ListDeploymentSets(r.Context(), id.ActiveTenantID)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	out := make([]types.DeploymentSet, 0, len(sets))
	for i := range sets {
		members, err := d.Store.ListSetMembers(r.Context(), id.ActiveTenantID, sets[i].ID)
		if err != nil {
			httpserver.WriteError(w, err)
			return
		}
		out = append(out, toDeploymentSet(&sets[i], members))
	}
	httpserver.WriteJSON(w, http.StatusOK, types.DeploymentSetList{Sets: out})
}

// GetSet serves GET /api/tenants/{tenantId}/deployment-sets/{setId} (Reader).
// {setId} is a tenant-level id (not a {vmid}, so ResolveScope does not resolve
// it): the handler does its OWN tenant-filtered 404, copying GetDeployment — a
// cross-tenant or missing set is an indistinguishable 404 (no existence leak).
func (d *Deps) GetSet(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIdentity(w, r)
	if !ok || !d.requireStore(w) {
		return
	}
	if !d.setsReady(w) {
		return
	}
	set, ok := d.resolveSet(w, r, id.ActiveTenantID)
	if !ok {
		return
	}
	members, err := d.Store.ListSetMembers(r.Context(), id.ActiveTenantID, set.ID)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toDeploymentSet(set, members))
}

// CreateSet serves POST /api/tenants/{tenantId}/deployment-sets (Contributor). It
// resolves the project→pool (cross-tenant 404), renders every member's cloud-init
// and generates the shared join token, then reserves ALL members atomically
// (ReserveOwnershipBatch) BEFORE any Proxmox call, ensures the pool, and hands the
// prepared members to the engine's set orchestrator (server first, then agents,
// ADR-0030). The generated K3S_TOKEN never leaves the rendered snippets — it is
// not stored, logged, audited, or returned (ADR-0030).
func (d *Deps) CreateSet(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIdentity(w, r)
	if !ok || !d.requireStore(w) {
		return
	}
	if !d.setsReady(w) {
		return
	}
	// Degrade, don't crash: deployment sets place a cloud-init snippet for every
	// member, so they need the SSH/SFTP snippet writer. If it failed to initialize
	// at boot, fail honestly with 503 BEFORE any reserve/quota/deploy work — never
	// leak a batch reservation or half-provision a set (mirrors ProvisionService).
	if !d.CatalogProvisionReady {
		httpserver.WriteError(w, serviceUnavailable("deployment-set provisioning is unavailable: snippet writer is not configured"))
		return
	}
	if d.Deploy == nil {
		httpserver.WriteError(w, &types.APIError{Code: "internal", Message: "deployment engine not configured", Status: http.StatusInternalServerError})
		return
	}
	var req types.CreateSetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpserver.WriteError(w, badRequest("body must be a JSON CreateSetRequest"))
		return
	}
	tenantID := id.ActiveTenantID
	if req.ServiceId == "" {
		httpserver.WriteError(w, badRequest("serviceId is required"))
		return
	}
	if req.ProjectId == "" {
		httpserver.WriteError(w, badRequest("projectId is required"))
		return
	}
	svc, ok := d.Catalog.Get(req.ServiceId)
	if !ok {
		httpserver.WriteError(w, notFound("Service not found."))
		return
	}
	if !svc.IsSet() {
		httpserver.WriteError(w, badRequest("that service is not a deployment set"))
		return
	}

	// Resolve the project → pool. Cross-tenant project → 404 (no existence leak).
	proj, err := d.Store.GetProjectByID(r.Context(), req.ProjectId)
	if errors.Is(err, store.ErrNotFound) {
		httpserver.WriteError(w, notFound("Project not found."))
		return
	}
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	if proj.TenantID != tenantID {
		httpserver.WriteError(w, notFound("Project not found."))
		return
	}

	// Build the plan (renders each member's snippet + generates the join token)
	// BEFORE any reservation, so a bad request returns here with zero side effects.
	plan, err := d.buildSetProvision(svc, &req)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}

	actor := id.UserID
	snap, err := d.clusterSnapshot(r)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}

	// The set row must exist before the batch tags its member ownership rows (the FK
	// deployment_set_id → deployment_set(id)). It is a DB write, not a Proxmox call,
	// so the atomic reservation still precedes every Proxmox call.
	set, err := d.Store.CreateDeploymentSet(r.Context(), store.CreateDeploymentSetParams{
		TenantID: tenantID, ProjectID: proj.ID, ServiceID: svc.ID,
	})
	if err != nil {
		d.logger().Error("create deployment set", "service", svc.ID, "err", err)
		httpserver.WriteError(w, &types.APIError{Code: "internal", Message: "Failed to create the deployment set.", Status: http.StatusInternalServerError})
		return
	}

	reserved, err := d.Store.ReserveOwnershipBatch(r.Context(), store.ReserveOwnershipBatchParams{
		TenantID: tenantID, ProjectID: proj.ID, SetID: set.ID, CreatedBy: &actor,
		Snapshot: snap, Members: plan.batch,
	})
	if err != nil {
		// The batch is all-or-nothing: no member row survives. Remove the now-empty
		// set row so an over-quota create leaves ZERO pending rows and zero set rows.
		if delErr := d.Store.DeleteDeploymentSet(r.Context(), tenantID, set.ID); delErr != nil {
			d.logger().Warn("delete set after reserve failure", "set", set.ID, "err", delErr)
		}
		var qe store.ErrQuotaExceeded
		switch {
		case errors.As(err, &qe):
			httpserver.WriteError(w, &types.APIError{Code: "quota_exceeded", Message: quotaExceededMessage(qe), Status: http.StatusConflict})
		case errors.Is(err, store.ErrConflict):
			httpserver.WriteError(w, &types.APIError{Code: "conflict", Message: "One of the chosen VMIDs is already reserved or in use.", Status: http.StatusConflict})
		default:
			d.logger().Error("reserve set batch", "set", set.ID, "err", err)
			httpserver.WriteError(w, &types.APIError{Code: "internal", Message: "Failed to reserve the cluster VMIDs.", Status: http.StatusInternalServerError})
		}
		return
	}

	// Ensure the pool AFTER the reservation clears. A pool failure frees the whole
	// batch + the set row so nothing leaks.
	if err := bootstrap.EnsureProjectPool(r.Context(), d.PVE, proj.PoolID, poolComment); err != nil {
		d.releaseSetReservations(r.Context(), reserved)
		if delErr := d.Store.DeleteDeploymentSet(r.Context(), tenantID, set.ID); delErr != nil {
			d.logger().Warn("delete set after pool failure", "set", set.ID, "err", delErr)
		}
		httpserver.WriteError(w, err)
		return
	}

	// Wire each prepared member to its reserved ownership row + the resolved pool.
	ownByVMID := make(map[int]string, len(reserved))
	for _, o := range reserved {
		ownByVMID[o.VMID] = o.ID
	}
	for i := range plan.members {
		plan.members[i].OwnershipID = ownByVMID[plan.members[i].Req.VMID]
		plan.members[i].Req.Pool = proj.PoolID
	}

	sctx := deploy.SetContext{
		TenantID: tenantID, ProjectID: proj.ID, PoolID: proj.PoolID,
		ActorUserID: actor, ServiceID: svc.ID,
	}
	hooks := deploy.SetHooks{
		UpdateStatus: func(ctx context.Context, setID, status string) error {
			return d.Store.UpdateSetStatus(ctx, tenantID, setID, status)
		},
	}
	d.Deploy.SubmitSet(set.ID, plan.members, sctx, hooks)

	// Audit enrichment: non-secret facts only (set id, service, member count) —
	// NEVER the K3S_TOKEN (ADR-0030).
	authz.Annotate(r.Context(), "set", set.ID)
	authz.Annotate(r.Context(), "service", svc.ID)
	authz.Annotate(r.Context(), "member_count", strconv.Itoa(len(plan.views)))

	httpserver.WriteJSON(w, http.StatusAccepted, types.CreateSetResponse{
		SetID: set.ID, Status: set.Status, Members: plan.views,
	})
}

// SetAction serves POST /api/tenants/{tenantId}/deployment-sets/{setId}/{action}
// (Contributor) for start/stop. It fans the lifecycle action out over the set's
// members in ADR-0030 order (server first on start; agents first on stop) and
// returns the per-member Proxmox task refs the UI polls to completion. A per-member
// failure is logged and skipped (best-effort); only a fan-out that starts NOTHING
// surfaces the error.
func (d *Deps) SetAction(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIdentity(w, r)
	if !ok || !d.requireStore(w) {
		return
	}
	if !d.setsReady(w) {
		return
	}
	action := chi.URLParam(r, "action")
	if action != "start" && action != "stop" {
		httpserver.WriteError(w, notFound(fmt.Sprintf("unknown set action %q", action)))
		return
	}
	set, ok := d.resolveSet(w, r, id.ActiveTenantID)
	if !ok {
		return
	}
	members, err := d.Store.ListSetMembers(r.Context(), id.ActiveTenantID, set.ID)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	// start: server first (control plane before workers); stop: agents first.
	ordered := orderMembers(members, action == "stop")
	label := "Start cluster"
	transitional := "starting"
	if action == "stop" {
		label, transitional = "Stop cluster", "stopping"
	}
	tasks := []types.TaskRef{}
	var lastErr error
	for _, m := range ordered {
		if m.Status == "tombstoned" {
			continue
		}
		ref := proxmox.GuestRef{Node: m.Node, Type: m.GuestType, VMID: m.VMID}
		upid, err := d.PVE.GuestAction(r.Context(), ref, action)
		if err != nil {
			lastErr = err
			d.logger().Warn("set action member", "set", set.ID, "vmid", m.VMID, "action", action, "err", err)
			continue
		}
		res := types.TaskResource{Type: m.GuestType, VMID: m.VMID, Node: m.Node}
		d.trackRes(upid, label, transitional, res)
		tasks = append(tasks, types.TaskRef{UPID: string(upid), Action: label})
	}
	// If nothing started and we hit an error, surface it (e.g. PVE unreachable).
	if len(tasks) == 0 && lastErr != nil {
		httpserver.WriteError(w, lastErr)
		return
	}
	authz.Annotate(r.Context(), "set", set.ID)
	authz.Annotate(r.Context(), "member_count", strconv.Itoa(len(tasks)))
	httpserver.WriteJSON(w, http.StatusAccepted, types.SetActionResponse{SetID: set.ID, Tasks: tasks})
}

// DeleteSet serves DELETE /api/tenants/{tenantId}/deployment-sets/{setId}
// (Contributor): reverse teardown (agents before the server, ADR-0030). Each member
// is destroyed (purge) and, on task success, its ownership row is tombstoned — the
// VMID freed — via the single-guest tombstone-after-destroy path, and its snippet
// removed (ADR-0025). Successful members are NEVER auto-destroyed elsewhere
// (ADR-0029); this is the one teardown path.
//
// The set row is removed ONLY when every member is torn down (its destroy submitted,
// or it was already tombstoned/absent). If ANY member's destroy fails to submit, the
// guest keeps running (and quota-charged), so the set row is KEPT with an honest
// 'deleting' status and the caller gets a partial-failure error (never a 202 "all
// good") — the delete is retryable, and members already submitted still tombstone.
func (d *Deps) DeleteSet(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIdentity(w, r)
	if !ok || !d.requireStore(w) {
		return
	}
	if !d.setsReady(w) {
		return
	}
	tenantID := id.ActiveTenantID
	set, ok := d.resolveSet(w, r, tenantID)
	if !ok {
		return
	}
	members, err := d.Store.ListSetMembers(r.Context(), tenantID, set.ID)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}

	// Durable status for the duration of the teardown (best-effort).
	if err := d.Store.UpdateSetStatus(r.Context(), tenantID, set.ID, "deleting"); err != nil {
		d.logger().Warn("mark set deleting", "set", set.ID, "err", err)
	}

	// Reverse teardown: agents before the server, so workers deregister before the
	// control plane they depend on disappears (ADR-0030). allTornDown tracks whether
	// EVERY member was destroyed (submitted) or already tombstoned/absent — only then
	// is it safe to remove the set row.
	ordered := orderMembers(members, true)
	tasks := []types.TaskRef{}
	allTornDown := true
	var lastErr error
	for _, m := range ordered {
		if m.Status == "tombstoned" {
			continue // already torn down / VMID freed — nothing to destroy
		}
		ref := proxmox.GuestRef{Node: m.Node, Type: m.GuestType, VMID: m.VMID}
		upid, err := d.PVE.DeleteGuest(r.Context(), ref, true) // purge
		if err != nil {
			// The guest is still running (and still quota-charged): do NOT tombstone
			// it, and do NOT let the set row be deleted — leave an honest retry path.
			allTornDown = false
			lastErr = err
			d.logger().Warn("set delete member", "set", set.ID, "vmid", m.VMID, "err", err)
			continue
		}
		res := types.TaskResource{Type: m.GuestType, VMID: m.VMID, Node: m.Node}
		d.trackRes(upid, "Delete cluster member", "deleting", res)
		// Reuse the single-guest teardown: on destroy success the member's ownership
		// row is tombstoned (freeing the VMID) and its scheduler jobs cancelled.
		if d.Registry != nil {
			go d.tombstoneOwnershipAfterDestroy(m.VMID, upid)
		}
		d.removeSetMemberSnippet(set.ServiceID, m.VMID)
		tasks = append(tasks, types.TaskRef{UPID: string(upid), Action: "Delete cluster member"})
	}

	authz.Annotate(r.Context(), "set", set.ID)
	authz.Annotate(r.Context(), "member_count", strconv.Itoa(len(tasks)))

	if !allTornDown {
		// Partial teardown: keep the set row so the orphaned member(s) stay visible and
		// the delete can be retried; report the real Proxmox failure (not a 202).
		if err := d.Store.UpdateSetStatus(r.Context(), tenantID, set.ID, "deleting"); err != nil {
			d.logger().Warn("mark set deleting after partial teardown", "set", set.ID, "err", err)
		}
		httpserver.WriteError(w, &types.APIError{
			Code:    "set_teardown_incomplete",
			Message: fmt.Sprintf("The cluster was only partially torn down: at least one member could not be deleted and is still running. The set was kept for retry. Last Proxmox error: %v", lastErr),
			Status:  http.StatusBadGateway,
		})
		return
	}

	// Every member is torn down. Remove the set row last. Members' set linkage is
	// nulled by the FK's ON DELETE SET NULL; their ownership rows tombstone
	// asynchronously as the destroys land.
	if err := d.Store.DeleteDeploymentSet(r.Context(), tenantID, set.ID); err != nil {
		d.logger().Warn("delete set row", "set", set.ID, "err", err)
	}
	httpserver.WriteJSON(w, http.StatusAccepted, types.SetActionResponse{SetID: set.ID, Tasks: tasks})
}

// resolveSet loads a set and enforces the tenant-filtered 404 (the tenancy iron
// rule: cross-tenant or missing → an indistinguishable 404, never 403).
func (d *Deps) resolveSet(w http.ResponseWriter, r *http.Request, tenantID string) (*store.DeploymentSet, bool) {
	setID := chi.URLParam(r, "setId")
	set, err := d.Store.GetDeploymentSet(r.Context(), tenantID, setID)
	if errors.Is(err, store.ErrNotFound) {
		httpserver.WriteError(w, notFound("Deployment set not found."))
		return nil, false
	}
	if err != nil {
		httpserver.WriteError(w, err)
		return nil, false
	}
	if set.TenantID != tenantID {
		httpserver.WriteError(w, notFound("Deployment set not found."))
		return nil, false
	}
	return set, true
}

// releaseSetReservations frees every reserved member row (a pre-Proxmox failure
// after ReserveOwnershipBatch, e.g. the pool ensure), so no quota leaks.
func (d *Deps) releaseSetReservations(ctx context.Context, reserved []store.ResourceOwnership) {
	for _, o := range reserved {
		if err := d.Store.ReleaseOwnership(ctx, o.ID); err != nil {
			d.logger().Warn("release set reservation", "ownership", o.ID, "err", err)
		}
	}
}

// removeSetMemberSnippet best-effort deletes a member's cloud-init snippet on
// teardown (ADR-0025). The filename is reconstructed from the same convention
// buildSetProvision used (proxcloud-<vmid>-<service>.yaml). Runs detached — a
// delete returns 202 and must not block on SFTP.
func (d *Deps) removeSetMemberSnippet(serviceID string, vmid int) {
	if d.Deploy == nil {
		return
	}
	filename := fmt.Sprintf("proxcloud-%d-%s.yaml", vmid, serviceID)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), snippetRemoveTimeout)
		defer cancel()
		if err := d.Deploy.RemoveSnippet(ctx, filename); err != nil {
			d.logger().Warn("remove set member snippet", "vmid", vmid, "file", filename, "err", err)
		}
	}()
}

// setProvision bundles what CreateSet needs after rendering: the engine members
// (ordered inputs, each with its rendered snippet), the atomic-reservation batch,
// and the response/SSE member views.
type setProvision struct {
	members []deploy.SetMember
	batch   []store.BatchMember
	views   []types.DeploymentSetMember
}

// buildSetProvision assembles a K3s cluster provision from the service definition +
// request (ADR-0029/0030): it validates the member counts + VMIDs, requires the
// static control-plane IP (a joinable cluster needs a fixed address) and an SSH key
// (nodes lock password login), generates the shared K3S_TOKEN with crypto/rand, and
// renders each role's cloud-init with the token as a base64 blob only + the server's
// static IP so agents embed the join URL. Everything here precedes any reservation,
// so a bad request returns with zero side effects. The raw token is used ONLY to
// build the base64 transport and is never returned, logged, stored, or audited.
func (d *Deps) buildSetProvision(svc *catalog.ServiceDef, req *types.CreateSetRequest) (*setProvision, error) {
	if !svc.IsSet() {
		return nil, badRequest("that service is not a deployment set")
	}
	server, ok := svc.Role("server")
	if !ok {
		return nil, &types.APIError{Code: "internal", Message: "set service has no server role", Status: http.StatusInternalServerError}
	}
	agent, ok := svc.Role("agent")
	if !ok {
		return nil, &types.APIError{Code: "internal", Message: "set service has no agent role", Status: http.StatusInternalServerError}
	}

	// Resolve + bound the worker count (0 → the role default).
	count := req.AgentCount
	if count == 0 {
		count = agent.Count
	}
	minA, maxA := agent.Min, agent.Max
	if minA == 0 {
		minA = agent.Count
	}
	if maxA == 0 {
		maxA = agent.Count
	}
	if count < minA || count > maxA {
		return nil, badRequest(fmt.Sprintf("agentCount must be between %d and %d for %s", minA, maxA, svc.ID))
	}
	if len(req.AgentVMIDs) != count {
		return nil, badRequest(fmt.Sprintf("agentVmids must list exactly %d VMIDs (one per worker)", count))
	}

	// Static control-plane IP (ADR-0030): a joinable cluster needs a fixed address
	// so agents can embed the join URL before any guest boots.
	if req.ServerIP == nil || req.ServerIP.Mode != "static" || strings.TrimSpace(req.ServerIP.CIDR) == "" {
		return nil, badRequest("a static serverIp (e.g. 192.168.1.50/24 plus a gateway) is required — a K3s control plane needs a fixed, joinable address")
	}
	serverHost := ipHostFromCIDR(req.ServerIP.CIDR)
	if serverHost == "" {
		return nil, badRequest("serverIp.cidr must be CIDR notation, e.g. 192.168.1.50/24")
	}

	// A cluster node locks password login, so an SSH key is the only way in.
	if !hasSSHKey(req.SSHKeys) {
		return nil, badRequest("at least one SSH public key is required — cluster nodes lock password login, so an SSH key is the only way in")
	}

	// Generate the shared cluster join token (crypto/rand). It is base64-transported
	// into every member's snippet and never leaves them (ADR-0030).
	token, err := generateK3sToken()
	if err != nil {
		d.logger().Error("generate k3s token", "service", svc.ID, "err", err)
		return nil, &types.APIError{Code: "internal", Message: "Failed to prepare the cluster join token.", Status: http.StatusInternalServerError}
	}
	tokenB64 := catalog.B64(token)
	keysB64 := catalog.B64Each(req.SSHKeys)
	port := svc.PrimaryPort()

	p := &setProvision{}
	// Server member (static IP; readiness = the API port answers).
	if err := d.appendSetMember(p, svc, req, "server", req.Name+"-server", req.ServerVMID, server.Sizing.Default, req.ServerIP, serverHost, tokenB64, keysB64, port, port); err != nil {
		return nil, err
	}
	// Agent members (DHCP; readiness = a routable IP, i.e. the OS booted — an agent
	// does not serve the API port, so there is no port to probe honestly).
	for i, vmid := range req.AgentVMIDs {
		name := fmt.Sprintf("%s-agent-%d", req.Name, i+1)
		if err := d.appendSetMember(p, svc, req, "agent", name, vmid, agent.Sizing.Default, &types.IPConfig{Mode: "dhcp"}, serverHost, tokenB64, keysB64, port, 0); err != nil {
			return nil, err
		}
	}
	return p, nil
}

// appendSetMember assembles + validates one member's CreateGuestRequest, renders
// its role cloud-init, and appends the member/batch/view triple to the plan.
func (d *Deps) appendSetMember(p *setProvision, svc *catalog.ServiceDef, req *types.CreateSetRequest, role, name string, vmid int, sz catalog.Size, ipcfg *types.IPConfig, serverHost, tokenB64 string, keysB64 []string, port, readiness int) error {
	filename := fmt.Sprintf("proxcloud-%d-%s.yaml", vmid, svc.ID)
	snippetRef := d.SnippetDatastore + ":snippets/" + filename
	// Only the control plane exposes the API on the readiness port; agents do not.
	ports := svc.Ports
	if role != "server" {
		ports = nil
	}
	cg := types.CreateGuestRequest{
		Type: "qemu", Name: name, Node: req.Node, VMID: vmid, ProjectId: req.ProjectId,
		Source:   types.CreateSource{Mode: "image", ImageVolID: svc.BaseImage.Ref},
		Cores:    sz.Cores,
		MemoryMB: sz.MemoryMb,
		DiskGB:   sz.DiskGb,
		Storage:  req.Storage,
		Bridge:   req.Bridge,
		VLANTag:  req.VLANTag,
		Firewall: req.Firewall,
		IPConfig: ipcfg,
		Tags:     req.Tags,
		// A cluster node must start so the configuring step can reach it.
		StartAfterCreate: true,
		Catalog: &types.CatalogProvision{
			ServiceID:      svc.ID,
			SnippetRef:     snippetRef,
			Ports:          ports,
			CredentialHint: "cluster join token generated server-side — see next steps for the kubeconfig",
			UserSupplied:   false,
		},
	}
	// Validate the assembled request BEFORE rendering, so an invalid member name
	// (which becomes the guest hostname) is rejected before it is interpolated.
	if err := deploy.Validate(&cg); err != nil {
		return badRequest(err.Error())
	}
	content, err := svc.RenderRoleCloudInit(role, catalog.SetCloudInitInput{
		Hostname:    name,
		LoginUser:   "proxcloud",
		SSHKeysB64:  keysB64,
		K3sTokenB64: tokenB64,
		ServerIP:    serverHost,
		Port:        port,
	})
	if err != nil {
		d.logger().Error("render set cloud-init", "service", svc.ID, "role", role, "err", err)
		return &types.APIError{Code: "internal", Message: "Failed to render the cluster configuration.", Status: http.StatusInternalServerError}
	}
	p.members = append(p.members, deploy.SetMember{
		Role: role, Req: &cg, SnippetContent: content, SnippetFilename: filename, ReadinessPort: readiness,
	})
	p.batch = append(p.batch, store.BatchMember{
		VMID: vmid, GuestType: "qemu", Node: req.Node, Role: role,
		Reserved: store.Alloc{VCPU: sz.Cores, RAMMB: sz.MemoryMb, DiskGB: int64(sz.DiskGb)},
	})
	p.views = append(p.views, types.DeploymentSetMember{
		Role: role, VMID: vmid, Name: name, Node: req.Node, GuestType: "qemu", Status: "pending",
	})
	return nil
}

// toDeploymentSet builds the frontend view of a set from its row + member ownership
// rows. Member Status is the durable ownership status (pending | active |
// tombstoned) — the per-member honesty the set aggregates; it never carries a secret.
func toDeploymentSet(set *store.DeploymentSet, members []store.ResourceOwnership) types.DeploymentSet {
	ms := make([]types.DeploymentSetMember, 0, len(members))
	for _, m := range members {
		role := ""
		if m.Role != nil {
			role = *m.Role
		}
		ms = append(ms, types.DeploymentSetMember{
			Role: role, VMID: m.VMID, Node: m.Node, GuestType: m.GuestType, Status: m.Status,
		})
	}
	return types.DeploymentSet{
		ID: set.ID, ServiceID: set.ServiceID, ProjectId: set.ProjectID,
		Status: set.Status, CreatedAt: set.CreatedAt, Members: ms,
	}
}

// orderMembers orders a set's members for provisioning/start (server first) or,
// when reverse is true, for stop/teardown (agents first) — ADR-0030.
func orderMembers(members []store.ResourceOwnership, reverse bool) []store.ResourceOwnership {
	out := append([]store.ResourceOwnership(nil), members...)
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := memberRoleRank(out[i]), memberRoleRank(out[j])
		if ri == rj {
			return out[i].VMID < out[j].VMID
		}
		if reverse {
			return ri > rj
		}
		return ri < rj
	})
	return out
}

// memberRoleRank ranks an ownership row's role for ordering (server before agent).
func memberRoleRank(o store.ResourceOwnership) int {
	if o.Role == nil {
		return 2
	}
	switch *o.Role {
	case "server":
		return 0
	case "agent":
		return 1
	default:
		return 2
	}
}

// generateK3sToken mints the cluster's shared join secret with crypto/rand (32
// bytes, hex). It is transported into member snippets ONLY as a base64 blob
// (ADR-0027/0030) and never stored, logged, audited, or returned.
func generateK3sToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// ipHostFromCIDR extracts the host from "192.168.1.50/24" → "192.168.1.50",
// returning "" for a non-CIDR/invalid value (the member request's deploy.Validate
// does the strict CIDR check).
func ipHostFromCIDR(cidr string) string {
	i := strings.IndexByte(cidr, '/')
	if i <= 0 {
		return ""
	}
	host := cidr[:i]
	if net.ParseIP(host) == nil {
		return ""
	}
	return host
}
