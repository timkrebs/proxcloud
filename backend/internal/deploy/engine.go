package deploy

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/events"
	"github.com/timkrebs9/proxcloud/backend/internal/proxmox"
	"github.com/timkrebs9/proxcloud/backend/internal/tasks"
)

// stepTimeout bounds one deployment step (large full clones can be slow).
const stepTimeout = 30 * time.Minute

// Defaults for the catalog `configuring` step (ADR-0028); overridable on the
// Engine for tests. configureTimeout bounds the whole wait-for-IP + probe;
// configurePoll is the AgentInterfaces poll interval; probeTimeout bounds one
// TCP readiness dial; snippetWriteTimeout bounds one SSH/SFTP snippet op.
const (
	configureTimeoutDefault = 15 * time.Minute
	configurePollDefault    = 5 * time.Second
	probeTimeoutDefault     = 5 * time.Second
	snippetWriteTimeout     = 45 * time.Second
	agentReadTimeout        = 10 * time.Second
)

// SnippetWriter delivers a rendered cloud-init snippet to the node before the
// guest's first boot and removes it on teardown (ADR-0025). The concrete
// implementation is proxmox.SnippetWriter (SSH/SFTP); the engine depends on this
// interface so tests inject a fake.
type SnippetWriter interface {
	WriteSnippet(ctx context.Context, filename, content string) error
	RemoveSnippet(ctx context.Context, filename string) error
}

// CreateContext carries the tenancy context of a create from the handler into
// the engine WITHOUT giving deploy a store dependency. The pool is applied via
// the existing req.Pool → p["pool"] passthrough; the ownership reservation is
// settled through the Finalize/Release hooks (wired to the store in main.go).
type CreateContext struct {
	TenantID      string
	ProjectID     string
	PoolID        string
	ActorUserID   string
	OwnershipID   string // the pending resource_ownership row to finalize/release
	CloneSourceOK bool   // clone-source ownership was verified by the handler

	// Catalog snippet delivery (ADR-0025). SnippetContent is the rendered
	// #cloud-config the engine writes BEFORE CreateVM (write failure fails the
	// deployment before any Proxmox call); SnippetFilename is the confined name it
	// is written/removed under. ReadinessPort is the catalog `readiness` target
	// the `configuring` step TCP-probes (0 = no probe). These are carried
	// out-of-band (never in the request JSON) so a credential-bearing snippet body
	// never rides the wire type.
	SnippetContent  string
	SnippetFilename string
	ReadinessPort   int
}

// createCtxKey carries the CreateContext on the engine's request context so a
// future hook (audit, quota) can read it; deploy itself never inspects the store.
type createCtxKey struct{}

// Engine executes deployments: create/clone (+ optional start) with live
// per-step progress. Deployments are kept in memory — the guest itself and
// the Proxmox task log remain the durable truth.
type Engine struct {
	PVE      proxmox.Client
	Registry *tasks.Registry
	Broker   *events.Broker
	Log      *slog.Logger

	// Snippets delivers/removes catalog cloud-init snippets (ADR-0025). Nil for
	// bare-guest deployments; required for catalog deployments (a nil writer on a
	// catalog deploy fails the prepare step honestly).
	Snippets SnippetWriter

	// Catalog `configuring` step tuning (ADR-0028). Zero uses the package
	// defaults; tests set small values to keep the poll fast.
	ConfigureTimeout time.Duration
	ConfigurePoll    time.Duration
	ProbeTimeout     time.Duration

	// Probe dials a readiness target (ip, port) with a bounded timeout; a nil
	// error means the port accepted a connection. Nil uses the real tcpProbe;
	// tests inject a deterministic probe so the `configuring` step does not depend
	// on a live listener.
	Probe func(ip string, port int, timeout time.Duration) error

	// Finalize/Release settle a create's pending ownership reservation. Both are
	// optional (nil in tests) and keep deploy free of a store dependency:
	// main.go wires them to store.FinalizeOwnership / store.ReleaseOwnership.
	Finalize func(ctx context.Context, ownershipID, upid string) error
	Release  func(ctx context.Context, ownershipID string) error

	mu   sync.RWMutex
	runs map[string]*types.Deployment
}

func (e *Engine) configureTimeout() time.Duration {
	if e.ConfigureTimeout > 0 {
		return e.ConfigureTimeout
	}
	return configureTimeoutDefault
}

func (e *Engine) configurePoll() time.Duration {
	if e.ConfigurePoll > 0 {
		return e.ConfigurePoll
	}
	return configurePollDefault
}

func (e *Engine) probeTimeout() time.Duration {
	if e.ProbeTimeout > 0 {
		return e.ProbeTimeout
	}
	return probeTimeoutDefault
}

// NewEngine returns an empty engine.
func NewEngine(pve proxmox.Client, reg *tasks.Registry, broker *events.Broker, log *slog.Logger) *Engine {
	return &Engine{PVE: pve, Registry: reg, Broker: broker, Log: log, runs: map[string]*types.Deployment{}}
}

// Get returns a deployment snapshot by id.
func (e *Engine) Get(id string) (*types.Deployment, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	d, ok := e.runs[id]
	if !ok {
		return nil, false
	}
	cp := *d
	cp.Steps = append([]types.DeploymentStep(nil), d.Steps...)
	return &cp, true
}

// Submit validates the request and starts the deployment goroutine. cctx carries
// the tenancy context (pool passthrough + ownership reservation to settle).
func (e *Engine) Submit(req *types.CreateGuestRequest, cctx CreateContext) (*types.Deployment, error) {
	if err := Validate(req); err != nil {
		return nil, &types.APIError{Code: "invalid_request", Message: err.Error(), Status: 400}
	}

	kind := "virtual machine"
	if req.Type == "lxc" {
		kind = "container"
	}
	createLabel := fmt.Sprintf("Create %s %s", kind, req.Name)
	if req.Source.Mode == "clone" {
		createLabel = fmt.Sprintf("Clone VMID %d into %s", req.Source.CloneVMID, req.Name)
	}

	buf := make([]byte, 6)
	_, _ = rand.Read(buf)
	dep := &types.Deployment{
		ID:        "dep_" + hex.EncodeToString(buf),
		Name:      req.Name,
		Type:      req.Type,
		Node:      req.Node,
		VMID:      req.VMID,
		Status:    "running",
		CreatedAt: time.Now().UTC(),
	}
	// A catalog deployment writes its cloud-init snippet BEFORE the create call, so
	// the "prepare" step runs first (ADR-0025).
	if req.Catalog != nil {
		dep.Steps = append(dep.Steps, types.DeploymentStep{
			Key: "prepare", Label: fmt.Sprintf("Prepare cloud-init for %s", req.Name), Status: "pending",
		})
	}
	dep.Steps = append(dep.Steps, types.DeploymentStep{Key: "create", Label: createLabel, Status: "pending"})
	if req.StartAfterCreate {
		dep.Steps = append(dep.Steps, types.DeploymentStep{
			Key: "start", Label: fmt.Sprintf("Start %s %s", kind, req.Name), Status: "pending",
		})
	}
	// A catalog deployment is not "ready" at power-on: the "configuring" step waits
	// for the guest to boot and its service port to come up (ADR-0028).
	if req.Catalog != nil {
		label := fmt.Sprintf("Configure %s", req.Name)
		if req.Catalog.ServiceID != "" {
			label = fmt.Sprintf("Configure %s (%s)", req.Name, req.Catalog.ServiceID)
		}
		dep.Steps = append(dep.Steps, types.DeploymentStep{Key: "configuring", Label: label, Status: "pending"})
	}

	e.mu.Lock()
	e.runs[dep.ID] = dep
	e.mu.Unlock()

	go e.run(dep.ID, req, cctx)
	snapshot, _ := e.Get(dep.ID)
	return snapshot, nil
}

// run executes the deployment steps sequentially.
func (e *Engine) run(id string, req *types.CreateGuestRequest, cctx CreateContext) {
	ctx := context.WithValue(context.Background(), createCtxKey{}, cctx)
	res := types.TaskResource{Type: req.Type, VMID: req.VMID, Node: req.Node, Name: req.Name}

	submitCreate := func() (proxmox.UPID, error) {
		if req.Source.Mode == "clone" {
			cp, err := BuildCloneParams(req)
			if err != nil {
				return "", &types.APIError{Code: "invalid_request", Message: err.Error(), Status: 400}
			}
			src := proxmox.GuestRef{Node: req.Source.CloneNode, Type: "qemu", VMID: req.Source.CloneVMID}
			if src.Node == "" {
				src.Node = req.Node
			}
			return e.PVE.CloneGuest(ctx, src, cp.NewVMID, cp.Name, cp.Pool, cp.Full, cp.Storage)
		}
		params, err := BuildCreateParams(req)
		if err != nil {
			return "", &types.APIError{Code: "invalid_request", Message: err.Error(), Status: 400}
		}
		if req.Type == "lxc" {
			return e.PVE.CreateLXC(ctx, req.Node, params)
		}
		return e.PVE.CreateVM(ctx, req.Node, params)
	}

	isCatalog := req.Catalog != nil

	// Catalog: render/write the snippet BEFORE CreateVM. A write failure fails the
	// deployment before any Proxmox create call, releasing the reservation — the
	// same failure discipline as a rejected create (ADR-0025).
	if isCatalog {
		if !e.prepareSnippet(id, cctx) {
			e.releaseOwnership(cctx)
			e.finish(id, "failed")
			return
		}
	}

	label := e.stepLabel(id, "create")
	upid, err := submitCreate()
	if err != nil {
		e.releaseOwnership(cctx)
		e.removeSnippet(cctx) // clean up the just-written snippet
		e.failStep(id, "create", err)
		return
	}
	e.Registry.Track(upid, label, "provisioning", res)
	e.updateStep(id, "create", "running", string(upid), "")
	if !e.awaitTask(id, "create", upid) {
		e.releaseOwnership(cctx)
		e.removeSnippet(cctx)
		return
	}
	// The guest now exists: finalize its ownership reservation (pending → active).
	e.finalizeOwnership(cctx, string(upid))

	if req.StartAfterCreate {
		ref := proxmox.GuestRef{Node: req.Node, Type: req.Type, VMID: req.VMID}
		startUPID, err := e.PVE.GuestAction(ctx, ref, "start")
		if err != nil {
			e.removeSnippet(cctx)
			e.failStep(id, "start", err)
			return
		}
		e.Registry.Track(startUPID, e.stepLabel(id, "start"), "starting", res)
		e.updateStep(id, "start", "running", string(startUPID), "")
		if !e.awaitTask(id, "start", startUPID) {
			e.removeSnippet(cctx)
			return
		}
	}

	// Catalog: never declare ready at power-on — wait for the guest to boot and
	// its service port to answer (ADR-0028), then surface the connection details.
	if isCatalog {
		e.configure(id, req, cctx)
		return
	}

	e.finish(id, "succeeded")
}

// prepareSnippet renders-and-writes the catalog snippet before the create call.
// It marks the "prepare" step succeeded/failed with the real writer error and
// returns whether to proceed. A nil writer on a catalog deploy is a fail-closed
// misconfiguration, surfaced honestly rather than silently skipped.
func (e *Engine) prepareSnippet(id string, cctx CreateContext) bool {
	e.updateStep(id, "prepare", "running", "", "")
	if e.Snippets == nil {
		e.updateStep(id, "prepare", "failed", "", "snippet writer is not configured (CATALOG_ENABLED without node SSH)")
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), snippetWriteTimeout)
	defer cancel()
	if err := e.Snippets.WriteSnippet(ctx, cctx.SnippetFilename, cctx.SnippetContent); err != nil {
		e.updateStep(id, "prepare", "failed", "", err.Error())
		return false
	}
	e.updateStep(id, "prepare", "succeeded", "", "")
	return true
}

// removeSnippet best-effort deletes the catalog snippet on a teardown/failure
// path (never on success — the file stays so a reboot re-reads it). No-op for
// bare guests or when no snippet was written.
func (e *Engine) removeSnippet(cctx CreateContext) {
	if e.Snippets == nil || cctx.SnippetFilename == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), snippetWriteTimeout)
	defer cancel()
	if err := e.Snippets.RemoveSnippet(ctx, cctx.SnippetFilename); err != nil {
		e.logger().Warn("remove snippet on teardown", "file", cctx.SnippetFilename, "err", err)
	}
}

// configure runs the "configuring" step (ADR-0028): wait for the guest agent to
// report a routable IP (proof the OS booted), then RETRY the readiness probe on a
// ticker until the port actually accepts a connection. It never fabricates
// readiness — reaching the timeout without a routable IP, or with a readiness port
// that never becomes reachable, marks the step (and deployment) FAILED with an
// honest message and removes the snippet. Only a genuinely reachable service
// reaches `ready` (CLAUDE.md rule 5; docs/proxmox/cloud-init.md §3.4).
func (e *Engine) configure(id string, req *types.CreateGuestRequest, cctx CreateContext) {
	e.updateStep(id, "configuring", "running", "", "")
	ref := proxmox.GuestRef{Node: req.Node, Type: req.Type, VMID: req.VMID}
	deadline := time.Now().Add(e.configureTimeout())

	ip, err := e.awaitGuestIP(ref, deadline)
	if err != nil {
		e.failConfigure(id, cctx, err.Error())
		return
	}

	// A service with a readiness port is not "ready" until that port answers.
	// Retry until the deadline; a timeout WITHOUT a passing probe is an honest
	// FAILURE, never a silent ready (ADR-0028).
	if cctx.ReadinessPort > 0 {
		if perr := e.awaitReadyPort(ip, cctx.ReadinessPort, deadline); perr != nil {
			e.failConfigure(id, cctx, perr.Error())
			return
		}
	}

	e.setConnection(id, ip, req.Catalog)
	e.updateStep(id, "configuring", "succeeded", "", "")
	e.finish(id, "succeeded")
}

// failConfigure fails the configuring step honestly: mark it failed with the real
// message, clean up the snippet, and fail the whole deployment. The create's
// ownership was already finalized (the guest exists on Proxmox), so it is NOT
// released here — a configuring failure means "the guest is up but not usable",
// not "the guest was never created".
func (e *Engine) failConfigure(id string, cctx CreateContext, msg string) {
	e.updateStep(id, "configuring", "failed", "", msg)
	e.removeSnippet(cctx)
	e.finish(id, "failed")
}

// awaitGuestIP polls AgentInterfaces until a routable IPv4 appears, treating
// ErrAgentUnavailable (and transient read errors) as "not yet". It is bounded by
// the shared configure deadline.
func (e *Engine) awaitGuestIP(ref proxmox.GuestRef, deadline time.Time) (string, error) {
	for {
		ctx, cancel := context.WithTimeout(context.Background(), agentReadTimeout)
		nics, err := e.PVE.AgentInterfaces(ctx, ref)
		cancel()
		if err == nil {
			if ip := firstRoutableIPv4(nics); ip != "" {
				return ip, nil
			}
		} else if !errors.Is(err, proxmox.ErrAgentUnavailable) {
			e.logger().Debug("configuring: agent interfaces read", "vmid", ref.VMID, "err", err)
		}
		if !time.Now().Before(deadline) {
			return "", fmt.Errorf("guest booted but no routable IP appeared within %s", e.configureTimeout())
		}
		time.Sleep(e.configurePoll())
	}
}

// awaitReadyPort retries a TCP probe of ip:port until it connects or the deadline
// passes. A deadline reached without a successful connect returns an honest error
// naming the port and budget — never a silent success.
func (e *Engine) awaitReadyPort(ip string, port int, deadline time.Time) error {
	for {
		if err := e.probe(ip, port); err == nil {
			return nil
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("guest booted at %s but port %d never became reachable within %s", ip, port, e.configureTimeout())
		}
		time.Sleep(e.configurePoll())
	}
}

// probe dials the readiness target, using the injected Probe when set (tests) and
// the real tcpProbe otherwise.
func (e *Engine) probe(ip string, port int) error {
	if e.Probe != nil {
		return e.Probe(ip, port, e.probeTimeout())
	}
	return tcpProbe(ip, port, e.probeTimeout())
}

// setConnection records the resolved coordinates on the deployment (ADR-0028).
// None of these carry a secret — CredentialHint is a hint, not the value.
func (e *Engine) setConnection(id, ip string, cat *types.CatalogProvision) {
	e.mu.Lock()
	if d, ok := e.runs[id]; ok {
		if cat != nil && len(cat.Ports) > 0 && cat.Ports[0] > 0 {
			d.Connection = net.JoinHostPort(ip, strconv.Itoa(cat.Ports[0]))
		} else {
			d.Connection = ip
		}
		if cat != nil {
			d.Ports = append([]int(nil), cat.Ports...)
			d.CredentialHint = cat.CredentialHint
		}
	}
	e.mu.Unlock()
	e.publish(id)
}

// firstRoutableIPv4 returns the first non-loopback, non-link-local IPv4 from the
// guest's reported NICs. Addresses arrive as "addr/prefix" (guest_config.go).
func firstRoutableIPv4(nics []types.GuestNIC) string {
	for _, n := range nics {
		for _, cidr := range n.IPv4 {
			ipStr := cidr
			if i := strings.IndexByte(cidr, '/'); i >= 0 {
				ipStr = cidr[:i]
			}
			ip := net.ParseIP(ipStr)
			if ip == nil || ip.To4() == nil {
				continue
			}
			if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
				continue
			}
			return ipStr
		}
	}
	return ""
}

// tcpProbe dials ip:port once with a bounded timeout; a successful connect is the
// readiness signal (docs/proxmox/cloud-init.md §3.4).
func tcpProbe(ip string, port int, timeout time.Duration) error {
	d := net.Dialer{Timeout: timeout}
	conn, err := d.Dial("tcp", net.JoinHostPort(ip, strconv.Itoa(port)))
	if err != nil {
		return err
	}
	return conn.Close()
}

// awaitTask waits for the tracked task to finish. The tasks.Watcher is
// the single PVE poller — its Complete() call delivers the outcome here,
// so the deployment page, the notification bell, and this engine all see
// the same result without double-polling Proxmox.
func (e *Engine) awaitTask(id, step string, upid proxmox.UPID) bool {
	ctx, cancel := context.WithTimeout(context.Background(), stepTimeout)
	defer cancel()
	outcome, err := e.Registry.AwaitCompletion(ctx, upid)
	if err != nil {
		e.updateStep(id, step, "failed", string(upid), "timed out waiting for the Proxmox task")
		e.finish(id, "failed")
		return false
	}
	if outcome.Succeeded {
		e.updateStep(id, step, "succeeded", string(upid), "")
		return true
	}
	e.updateStep(id, step, "failed", string(upid), outcome.ExitStatus)
	e.finish(id, "failed")
	return false
}

func (e *Engine) stepLabel(id, key string) string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	for _, s := range e.runs[id].Steps {
		if s.Key == key {
			return s.Label
		}
	}
	return key
}

func (e *Engine) failStep(id, key string, err error) {
	msg := err.Error()
	var apiErr *types.APIError
	if errors.As(err, &apiErr) {
		msg = apiErr.Message
		if apiErr.PVEMessage != "" {
			msg = apiErr.PVEMessage
		}
	}
	e.updateStep(id, key, "failed", "", msg)
	e.finish(id, "failed")
}

func (e *Engine) updateStep(id, key, status, upid, msg string) {
	now := time.Now().UTC()
	e.mu.Lock()
	if d, ok := e.runs[id]; ok {
		for i := range d.Steps {
			if d.Steps[i].Key != key {
				continue
			}
			d.Steps[i].Status = status
			if upid != "" {
				d.Steps[i].UPID = upid
			}
			if msg != "" {
				d.Steps[i].Message = msg
			}
			if status == "running" && d.Steps[i].StartedAt == nil {
				d.Steps[i].StartedAt = &now
			}
			if status == "succeeded" || status == "failed" {
				d.Steps[i].EndedAt = &now
			}
		}
	}
	e.mu.Unlock()
	e.publish(id)
}

func (e *Engine) finish(id, status string) {
	e.mu.Lock()
	if d, ok := e.runs[id]; ok {
		d.Status = status
	}
	e.mu.Unlock()
	e.publish(id)
}

func (e *Engine) publish(id string) {
	if e.Broker == nil {
		return
	}
	if d, ok := e.Get(id); ok {
		e.Broker.Publish(events.Event{Name: "deployment", Data: d})
	}
}

// ownershipCtxTimeout bounds the finalize/release store call — a short, bounded
// write that must not hang the deployment goroutine.
const ownershipCtxTimeout = 10 * time.Second

// finalizeOwnership settles a successful create's pending reservation
// (pending → active). No-op when no reservation was made or no hook is wired.
func (e *Engine) finalizeOwnership(cctx CreateContext, upid string) {
	if e.Finalize == nil || cctx.OwnershipID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), ownershipCtxTimeout)
	defer cancel()
	if err := e.Finalize(ctx, cctx.OwnershipID, upid); err != nil {
		e.logger().Warn("finalize ownership", "ownership", cctx.OwnershipID, "err", err)
	}
}

// releaseOwnership frees a failed create's pending reservation so its VMID can be
// reused. No-op when no reservation was made or no hook is wired.
func (e *Engine) releaseOwnership(cctx CreateContext) {
	if e.Release == nil || cctx.OwnershipID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), ownershipCtxTimeout)
	defer cancel()
	if err := e.Release(ctx, cctx.OwnershipID); err != nil {
		e.logger().Warn("release ownership", "ownership", cctx.OwnershipID, "err", err)
	}
}

func (e *Engine) logger() *slog.Logger {
	if e.Log != nil {
		return e.Log
	}
	return slog.Default()
}
