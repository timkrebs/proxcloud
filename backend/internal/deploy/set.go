package deploy

// Deployment-set orchestration (ADR-0029/0030). A set is one catalog action that
// provisions N linked guests sharing a lifecycle. SubmitSet REUSES the existing
// single-guest engine: it Submits each member and AwaitDeployment-s it to a
// terminal state, sequencing all 'server' members before any 'agent' (ADR-0030)
// so workers only boot once the control plane answers. The engine stays free of a
// store dependency — durable set status is written through the SetHooks.UpdateStatus
// callback (wired to store.UpdateSetStatus in main.go), exactly like Finalize/Release.

import (
	"context"
	"fmt"
	"sort"
	"time"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/events"
)

const (
	// setMemberTimeout bounds one member's whole create→start→configure run when
	// the orchestrator awaits it. It exceeds the engine's own per-step + configure
	// budget so the await never gives up before the member itself resolves.
	setMemberTimeout = stepTimeout + configureTimeoutDefault + 5*time.Minute
	// setStatusTimeout bounds one durable set-status write.
	setStatusTimeout = 10 * time.Second
)

// SetMember is one fully-prepared member of a deployment-set provision. The
// create request already carries the cicustom snippet ref; SnippetContent is the
// rendered #cloud-config (carrying the join token only as a base64 blob, ADR-0030)
// the engine writes before CreateVM. Role orders provisioning ('server' before
// 'agent'). OwnershipID is the pending reservation the engine settles per member.
type SetMember struct {
	Role            string // "server" | "agent"
	Req             *types.CreateGuestRequest
	SnippetContent  string
	SnippetFilename string
	ReadinessPort   int
	OwnershipID     string
}

// SetContext carries the tenancy + identity shared by all members of a set, plus
// the service id used to build the set's SSE frame. It mirrors CreateContext for
// the single-guest path.
type SetContext struct {
	TenantID    string
	ProjectID   string
	PoolID      string
	ActorUserID string
	ServiceID   string
}

// SetHooks let the orchestrator persist durable set status without giving deploy a
// store dependency (ADR-0029). UpdateStatus is wired to store.UpdateSetStatus in
// main.go, the same seam as the engine's Finalize/Release ownership hooks.
type SetHooks struct {
	UpdateStatus func(ctx context.Context, setID, status string) error
}

// SubmitSet provisions a deployment set asynchronously (ADR-0029/0030): it
// provisions all 'server' members first — awaiting each to ready (its configuring
// step passed and, for the server, Connection is set) — then all 'agent' members,
// then records the durable set status: ready (all up), degraded (server up but ≥1
// agent failed — a partial cluster), or failed (the control plane never came up).
// A failed server means the control plane never came up, so the still-unprovisioned
// agents are skipped and their reservations released (nothing to join). Successful
// members are NEVER auto-destroyed on failure (ADR-0029) — deleting the set is the
// one teardown path. Members are already quota-reserved by the handler
// (ReserveOwnershipBatch), so no quota decision happens here.
func (e *Engine) SubmitSet(setID string, members []SetMember, sctx SetContext, hooks SetHooks) {
	go e.runSet(setID, members, sctx, hooks)
}

func (e *Engine) runSet(setID string, members []SetMember, sctx SetContext, hooks SetHooks) {
	ordered := orderMembersForProvision(members)
	finals := map[int]*types.Deployment{}
	// Recover a panic anywhere in the longer multi-Proxmox set path so one bad member
	// step can't take down the whole backend (runSet is its own goroutine). Mark the
	// set failed (honest terminal state) — mirrors scheduler.runHandler's recover
	// (commit 2565212). The status write goes through hooks (no Broker), so the
	// recovery itself cannot re-panic on a panicking Broker.
	defer func() {
		if r := recover(); r != nil {
			e.logger().Error("set orchestration panic recovered", "set", setID, "panic", fmt.Sprint(r))
			e.updateSetStatus(hooks, setID, "failed")
		}
	}()
	e.publishSetFrame(setFrame(setID, sctx, "provisioning", members, finals))

	serverFailed := false
	anyFailed := false
	for _, m := range ordered {
		// Once the control plane failed, agents have nothing to join: free their
		// still-pending reservations (never submitted) and record them failed.
		if serverFailed && m.Role != "server" {
			e.releaseMember(m)
			finals[m.Req.VMID] = &types.Deployment{VMID: m.Req.VMID, Status: "failed"}
			anyFailed = true
			e.publishSetFrame(setFrame(setID, sctx, "provisioning", members, finals))
			continue
		}
		final := e.provisionSetMember(setID, m, sctx)
		finals[m.Req.VMID] = final
		if final.Status != "succeeded" {
			anyFailed = true
			if m.Role == "server" {
				serverFailed = true
			}
		}
		e.publishSetFrame(setFrame(setID, sctx, "provisioning", members, finals))
	}

	// Honest terminal status: a control-plane failure means the cluster never came
	// up at all (failed); the server up but ≥1 agent failed is a partial cluster
	// (degraded, a live-but-incomplete set); everything up is ready.
	status := "ready"
	switch {
	case serverFailed:
		status = "failed"
	case anyFailed:
		status = "degraded"
	}
	e.updateSetStatus(hooks, setID, status)
	e.publishSetFrame(setFrame(setID, sctx, status, members, finals))
}

// provisionSetMember submits one member through the existing engine and blocks
// until it reaches a terminal state, returning the final Deployment snapshot. A
// Submit rejected before it starts (validation) releases the member's reservation
// itself — the engine's run() only settles ownership for members it actually starts.
func (e *Engine) provisionSetMember(setID string, m SetMember, sctx SetContext) *types.Deployment {
	cctx := CreateContext{
		TenantID:        sctx.TenantID,
		ProjectID:       sctx.ProjectID,
		PoolID:          sctx.PoolID,
		ActorUserID:     sctx.ActorUserID,
		OwnershipID:     m.OwnershipID,
		SnippetContent:  m.SnippetContent,
		SnippetFilename: m.SnippetFilename,
		ReadinessPort:   m.ReadinessPort,
	}
	dep, err := e.Submit(m.Req, cctx)
	if err != nil {
		e.releaseMember(m)
		e.logger().Warn("set member submit rejected", "set", setID, "vmid", m.Req.VMID, "role", m.Role, "err", err)
		return &types.Deployment{VMID: m.Req.VMID, Status: "failed"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), setMemberTimeout)
	defer cancel()
	final, err := e.AwaitDeployment(ctx, dep.ID)
	if err != nil {
		e.logger().Warn("set member await", "set", setID, "vmid", m.Req.VMID, "role", m.Role, "err", err)
		if d, ok := e.Get(dep.ID); ok {
			return d
		}
		return &types.Deployment{VMID: m.Req.VMID, Status: "failed"}
	}
	return final
}

// releaseMember frees a member's pending reservation when it is skipped (a failed
// control plane) or rejected before start, reusing the engine's Release hook so no
// quota leaks. No-op without the hook or an ownership id.
func (e *Engine) releaseMember(m SetMember) {
	if e.Release == nil || m.OwnershipID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), ownershipCtxTimeout)
	defer cancel()
	if err := e.Release(ctx, m.OwnershipID); err != nil {
		e.logger().Warn("release skipped set member ownership", "ownership", m.OwnershipID, "err", err)
	}
}

func (e *Engine) updateSetStatus(hooks SetHooks, setID, status string) {
	if hooks.UpdateStatus == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), setStatusTimeout)
	defer cancel()
	if err := hooks.UpdateStatus(ctx, setID, status); err != nil {
		e.logger().Warn("update set status", "set", setID, "status", status, "err", err)
	}
}

// publishSetFrame emits a tenant-scoped deployment_set SSE frame (ADR-0029). The
// events.deliver() deployment_set case scopes it by the member VMIDs it names, so
// it never leaks a tenant's cluster topology. Nil-safe on a broker-less engine.
func (e *Engine) publishSetFrame(frame *types.DeploymentSet) {
	if e.Broker == nil {
		return
	}
	e.Broker.Publish(events.Event{Name: "deployment_set", Data: frame})
}

// setFrame builds the frontend view of a set from its members + their (partial)
// final deployments. It carries NO secret — the join token exists only inside the
// rendered member snippets (ADR-0030). Member VMIDs are the SSE scoping key.
func setFrame(setID string, sctx SetContext, status string, members []SetMember, finals map[int]*types.Deployment) *types.DeploymentSet {
	ms := make([]types.DeploymentSetMember, 0, len(members))
	for _, m := range members {
		mv := types.DeploymentSetMember{
			Role: m.Role, VMID: m.Req.VMID, Name: m.Req.Name, Node: m.Req.Node,
			GuestType: m.Req.Type, Status: "pending",
		}
		if fd, ok := finals[m.Req.VMID]; ok {
			switch fd.Status {
			case "succeeded":
				mv.Status = "active"
				mv.Connection = fd.Connection
			case "failed":
				mv.Status = "failed"
			default:
				mv.Status = "provisioning"
			}
		}
		ms = append(ms, mv)
	}
	return &types.DeploymentSet{
		ID: setID, ServiceID: sctx.ServiceID, ProjectId: sctx.ProjectID,
		Status: status, Members: ms,
	}
}

// orderMembersForProvision returns members ordered so all 'server' members precede
// any 'agent' (ADR-0030), stable within a role. Teardown reverses this.
func orderMembersForProvision(members []SetMember) []SetMember {
	out := append([]SetMember(nil), members...)
	sort.SliceStable(out, func(i, j int) bool {
		return roleRank(out[i].Role) < roleRank(out[j].Role)
	})
	return out
}

// roleRank orders roles for provisioning: server (control plane) first, then
// agents, then anything else.
func roleRank(role string) int {
	switch role {
	case "server":
		return 0
	case "agent":
		return 1
	default:
		return 2
	}
}
