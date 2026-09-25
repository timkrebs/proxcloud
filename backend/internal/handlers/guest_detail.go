package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/auth"
	"github.com/timkrebs9/proxcloud/backend/internal/httpserver"
	"github.com/timkrebs9/proxcloud/backend/internal/proxmox"
	"github.com/timkrebs9/proxcloud/backend/internal/store"
)

// diskKeyRe matches disk config keys: scsi0, virtio3, sata1, ide2, rootfs,
// mp0 (lxc mount points), efidisk0, tpmstate0.
var diskKeyRe = regexp.MustCompile(`^(scsi|virtio|sata|ide|mp)\d+$|^rootfs$|^efidisk\d+$|^tpmstate\d+$`)
var netKeyRe = regexp.MustCompile(`^net\d+$`)

// GetGuest serves GET /api/guests/{node}/{type}/{vmid}: status + parsed
// config as one detail document.
func (d *Deps) GetGuest(w http.ResponseWriter, r *http.Request) {
	ref, apiErr := guestRef(r)
	if apiErr != nil {
		httpserver.WriteError(w, apiErr)
		return
	}
	st, err := d.PVE.GuestStatus(r.Context(), ref)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	cfg, err := d.PVE.GuestConfig(r.Context(), ref)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}

	detail := types.GuestDetail{
		ID:        fmt.Sprintf("%s/%d", ref.Type, ref.VMID),
		Type:      ref.Type,
		VMID:      ref.VMID,
		Name:      st.Name,
		Node:      ref.Node,
		Status:    strings.ToLower(st.Status),
		UptimeSec: st.UptimeSec,
		CPUPct:    st.CPUPct,
		Cores:     st.Cores,
		MemUsed:   st.MemUsed,
		MemMax:    st.MemMax,
		Tags:      splitPVEList(cfgString(cfg, "tags")),
		Template:  cfgString(cfg, "template") == "1",

		Description: cfgString(cfg, "description"),
		Agent:       st.Agent,
		OnBoot:      cfgString(cfg, "onboot") == "1",
		OSType:      cfgString(cfg, "ostype"),
		Machine:     cfgString(cfg, "machine"),
		BootDisk:    cfgString(cfg, "bootdisk"),
		NICs:        parseNICs(cfg),
		Disks:       parseDisks(cfg),
		DiskRead:    st.DiskRead,
		DiskWrite:   st.DiskWrite,
		NetIn:       st.NetIn,
		NetOut:      st.NetOut,
	}
	// lxc: name lives in config "hostname"; qemu status usually carries it.
	if detail.Name == "" {
		if h := cfgString(cfg, "hostname"); h != "" {
			detail.Name = h
		} else {
			detail.Name = cfgString(cfg, "name")
		}
	}
	for _, disk := range detail.Disks {
		if !disk.CDROM {
			detail.DiskMax += disk.SizeBytes
		}
	}
	if d.Registry != nil {
		if transitional, upid, ok := d.Registry.ActiveFor(ref.VMID); ok {
			detail.Status = transitional
			detail.PendingTaskUPID = upid
		}
	}
	httpserver.WriteJSON(w, http.StatusOK, detail)
}

// UpdateGuestConfig serves PATCH /api/guests/{node}/{type}/{vmid}/config.
// Upper bounds on user-requested guest sizing, independent of quota: a sane
// ceiling so an absurd value (e.g. a 100000 GiB resize) is rejected outright.
const (
	maxMemoryMB = 4 << 20 // 4 TiB, in MiB
	maxDiskGiB  = 65536   // 64 TiB
)

// enforceGrowth rejects a cores/memory change or snapshot rollback that would
// push the caller's tenant/project past quota, and — when it fits — RESERVES
// the post-grow footprint on the guest's ownership row in the same
// advisory-locked transaction (store.ReserveGuestGrowth), so concurrent grows
// and creates cannot land in the same headroom. targetVCPU/targetRAMMB are the
// ABSOLUTE requested values (0 = unchanged). A request at or below the live
// size is not a grow and never reaches the store; everything else is charged
// against the guest's effective footprint inside the lock.
func (d *Deps) enforceGrowth(r *http.Request, ref proxmox.GuestRef, targetVCPU int, targetRAMMB int64) error {
	if targetVCPU <= 0 && targetRAMMB <= 0 {
		return nil // no quota dimension is being set
	}
	return d.reserveGrowth(r, ref, func(cur store.Alloc) (store.ReserveGrowthParams, bool) {
		grows := targetVCPU > cur.VCPU || targetRAMMB > cur.RAMMB
		return store.ReserveGrowthParams{TargetVCPU: targetVCPU, TargetRAMMB: targetRAMMB}, grows
	})
}

// enforceDiskGrowth is the disk-resize funnel. growGiB is how much the NAMED
// disk grows (measured by the caller against that disk's own configured size);
// it is charged in full on top of the guest's effective disk footprint. It is
// never converted to an absolute target — the counted footprint is the boot
// disk plus prior growth reservations, not any one disk's size.
func (d *Deps) enforceDiskGrowth(r *http.Request, ref proxmox.GuestRef, growGiB int64) error {
	if growGiB <= 0 {
		return nil // shrink/no-op: not a grow
	}
	return d.reserveGrowth(r, ref, func(store.Alloc) (store.ReserveGrowthParams, bool) {
		return store.ReserveGrowthParams{GrowDiskGB: growGiB}, true
	})
}

// reserveGrowth resolves the caller's scope and the cluster snapshot, lets
// build decide (against the guest's live allocation) whether this is a grow at
// all, then funnels it into the store reservation and maps the verdict onto
// the API contract (409 quota_exceeded on a cap hit). A snapshot miss on a real
// grow is a transient condition, rejected retryably.
func (d *Deps) reserveGrowth(r *http.Request, ref proxmox.GuestRef, build func(cur store.Alloc) (store.ReserveGrowthParams, bool)) error {
	if d.Store == nil {
		return nil // degraded/bootstrap: no quota store wired
	}
	id, ok := auth.IdentityFrom(r.Context())
	if !ok || id == nil || id.ActiveTenantID == "" || id.ResolvedProjectID == "" {
		return notFound("Resource not found.")
	}
	snap, err := d.clusterSnapshot(r)
	if err != nil {
		return err
	}
	cur, ok := snap[ref.VMID]
	if !ok {
		return &types.APIError{Code: "invalid_request", Message: "guest allocation is currently unavailable; try again", Status: http.StatusBadRequest}
	}
	p, grows := build(cur)
	if !grows {
		return nil
	}
	p.TenantID, p.ProjectID, p.VMID, p.Snapshot = id.ActiveTenantID, id.ResolvedProjectID, ref.VMID, snap
	if err := d.Store.ReserveGuestGrowth(r.Context(), p); err != nil {
		var qe store.ErrQuotaExceeded
		if errors.As(err, &qe) {
			return &types.APIError{Code: "quota_exceeded", Message: quotaExceededMessage(qe), Status: http.StatusConflict}
		}
		d.logger().Error("growth quota reservation", "vmid", ref.VMID, "err", err)
		return &types.APIError{Code: "internal", Message: "Failed to verify quota.", Status: http.StatusInternalServerError}
	}
	return nil
}

func (d *Deps) UpdateGuestConfig(w http.ResponseWriter, r *http.Request) {
	ref, apiErr := guestRef(r)
	if apiErr != nil {
		httpserver.WriteError(w, apiErr)
		return
	}
	var req types.UpdateConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpserver.WriteError(w, &types.APIError{Code: "invalid_request", Message: "body must be JSON", Status: http.StatusBadRequest})
		return
	}

	changes := map[string]any{}
	if req.Cores != nil {
		if *req.Cores < 1 || *req.Cores > 128 {
			httpserver.WriteError(w, &types.APIError{Code: "invalid_request", Message: "cores must be between 1 and 128", Status: http.StatusBadRequest})
			return
		}
		changes["cores"] = *req.Cores
	}
	if req.MemoryMB != nil {
		if *req.MemoryMB < 16 || *req.MemoryMB > maxMemoryMB {
			httpserver.WriteError(w, &types.APIError{Code: "invalid_request", Message: "memory must be between 16 MiB and 4 TiB", Status: http.StatusBadRequest})
			return
		}
		changes["memory"] = *req.MemoryMB
	}
	if req.Description != nil {
		changes["description"] = *req.Description
	}
	if req.OnBoot != nil {
		if *req.OnBoot {
			changes["onboot"] = 1
		} else {
			changes["onboot"] = 0
		}
	}
	if req.Tags != nil {
		for _, t := range *req.Tags {
			if !pveTagRe.MatchString(t) {
				httpserver.WriteError(w, &types.APIError{
					Code:    "invalid_request",
					Message: fmt.Sprintf("invalid tag %q — lowercase letters, digits, . - _ only", t),
					Status:  http.StatusBadRequest,
				})
				return
			}
		}
		changes["tags"] = strings.Join(*req.Tags, ";")
	}
	if len(changes) == 0 {
		httpserver.WriteError(w, &types.APIError{Code: "invalid_request", Message: "no changes provided", Status: http.StatusBadRequest})
		return
	}

	// Enforce quota on a cores/memory GROW (quota is otherwise only checked at
	// create — a Contributor must not be able to create small then grow past cap).
	var targetVCPU int
	var targetRAMMB int64
	if req.Cores != nil && d.Store != nil { // the target only matters where a quota store gates the change
		// Quota counts PVE's maxcpu, which for a VM is sockets × cores — so the
		// target must be too, or a multi-socket guest (e.g. one adopted by the
		// ownership backfill) grows by sockets×Δcores while being charged Δcores.
		var cfg map[string]any
		if ref.Type == "qemu" {
			c, err := d.PVE.GuestConfig(r.Context(), ref)
			if err != nil {
				httpserver.WriteError(w, err)
				return
			}
			cfg = c
			// Write only onto the config the charge was computed from: PVE
			// refuses the change if the config (sockets, say, via a snapshot
			// rollback) moved since this read, instead of applying the new
			// cores to more sockets than were charged.
			if digest, ok := c["digest"].(string); ok && digest != "" {
				changes["digest"] = digest
			}
		}
		v, err := guestVCPUTarget(ref.Type, cfg, *req.Cores)
		if err != nil {
			httpserver.WriteError(w, sizingUnreadable("guest configuration", err))
			return
		}
		targetVCPU = v
	}
	if req.MemoryMB != nil {
		targetRAMMB = *req.MemoryMB
	}
	if err := d.enforceGrowth(r, ref, targetVCPU, targetRAMMB); err != nil {
		httpserver.WriteError(w, err)
		return
	}

	upid, err := d.PVE.SetGuestConfig(r.Context(), ref, changes)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	if upid == "" { // lxc: synchronous
		w.WriteHeader(http.StatusNoContent)
		return
	}
	label := "Update virtual machine configuration"
	d.trackRes(upid, label, "resizing", types.TaskResource{Type: ref.Type, VMID: ref.VMID, Node: ref.Node}, activeTenantOf(r))
	httpserver.WriteJSON(w, http.StatusAccepted, types.TaskRef{UPID: string(upid), Action: label})
}

// pveTagRe is PVE's tag charset (lowercase alphanumerics plus . - _).
var pveTagRe = regexp.MustCompile(`^[a-z0-9_][a-z0-9_.-]*$`)

// GetGuestMetrics serves GET .../metrics?timeframe=.
func (d *Deps) GetGuestMetrics(w http.ResponseWriter, r *http.Request) {
	ref, apiErr := guestRef(r)
	if apiErr != nil {
		httpserver.WriteError(w, apiErr)
		return
	}
	timeframe := r.URL.Query().Get("timeframe")
	if timeframe == "" {
		timeframe = "hour"
	}
	switch timeframe {
	case "hour", "day", "week", "month", "year":
	default:
		httpserver.WriteError(w, &types.APIError{Code: "invalid_request", Message: "timeframe must be hour|day|week|month|year", Status: http.StatusBadRequest})
		return
	}
	series, err := d.PVE.GuestRRD(r.Context(), ref, timeframe)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, types.MetricsResponse{Timeframe: timeframe, Series: series})
}

// GetGuestInterfaces serves GET .../interfaces — live IPs, with the honest
// agent-unavailable state for qemu guests without the agent.
func (d *Deps) GetGuestInterfaces(w http.ResponseWriter, r *http.Request) {
	ref, apiErr := guestRef(r)
	if apiErr != nil {
		httpserver.WriteError(w, apiErr)
		return
	}
	nics, err := d.PVE.AgentInterfaces(r.Context(), ref)
	if err != nil {
		if errors.Is(err, proxmox.ErrAgentUnavailable) {
			httpserver.WriteJSON(w, http.StatusOK, types.GuestNICList{AgentUnavailable: true, NICs: []types.GuestNIC{}})
			return
		}
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, types.GuestNICList{NICs: nics})
}

// ResizeGuestDisk serves POST .../resize.
func (d *Deps) ResizeGuestDisk(w http.ResponseWriter, r *http.Request) {
	ref, apiErr := guestRef(r)
	if apiErr != nil {
		httpserver.WriteError(w, apiErr)
		return
	}
	var req types.ResizeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Disk == "" || req.SizeGiB < 1 || req.SizeGiB > maxDiskGiB {
		httpserver.WriteError(w, &types.APIError{Code: "invalid_request", Message: "disk and sizeGib (1..65536) are required", Status: http.StatusBadRequest})
		return
	}
	if !diskKeyRe.MatchString(req.Disk) {
		httpserver.WriteError(w, &types.APIError{Code: "invalid_request", Message: fmt.Sprintf("invalid disk key %q", req.Disk), Status: http.StatusBadRequest})
		return
	}

	// Measure the grow against the NAMED disk's own current size from the guest
	// config (`size=` attribute). Comparing against the boot-disk maxdisk (the
	// cluster snapshot) under-counts a non-boot-disk grow — e.g. growing a 10 GiB
	// data disk to 30 GiB on a guest with a 32 GiB boot disk would look like a
	// no-op. The per-disk delta is the honest quota charge.
	cfg, err := d.PVE.GuestConfig(r.Context(), ref)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	curGiB, apiErr := namedDiskSizeGiB(cfg, req.Disk)
	if apiErr != nil {
		httpserver.WriteError(w, apiErr)
		return
	}
	if err := d.enforceDiskGrowth(r, ref, int64(req.SizeGiB)-curGiB); err != nil {
		httpserver.WriteError(w, err)
		return
	}

	upid, err := d.PVE.ResizeDisk(r.Context(), ref, req.Disk, fmt.Sprintf("%dG", req.SizeGiB))
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	label := "Resize disk"
	if upid != "" {
		d.trackRes(upid, label, "resizing", types.TaskResource{Type: ref.Type, VMID: ref.VMID, Node: ref.Node}, activeTenantOf(r))
	}
	httpserver.WriteJSON(w, http.StatusAccepted, types.TaskRef{UPID: string(upid), Action: label})
}

// ── config-string parsing ────────────────────────────────────────────────────

// pveMemoryMB parses PVE's memory config value in MiB: a plain integer
// ("2048") or the newer property form ("current=2048"). Unparseable → 0
// (unknown, never invented).
func pveMemoryMB(v string) int64 {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		return n
	}
	_, opts := parsePVEValue(v)
	if cur := opts["current"]; cur != "" {
		if n, err := strconv.ParseInt(cur, 10, 64); err == nil {
			return n
		}
	}
	return 0
}

// cfgPositiveInt reads key as a positive integer. present=false with a nil
// error means the key is absent; a present value that is not a positive integer
// is an error, so a caller gating quota fails closed instead of treating
// garbage as "unchanged".
func cfgPositiveInt(cfg map[string]any, key string) (n int, present bool, err error) {
	if _, ok := cfg[key]; !ok {
		return 0, false, nil
	}
	s := strings.TrimSpace(cfgString(cfg, key))
	n, err = strconv.Atoi(s)
	if err != nil || n < 1 {
		return 0, true, fmt.Errorf("%s=%q is not a positive integer", key, s)
	}
	return n, true, nil
}

// cfgMemoryMB is cfgPositiveInt for PVE's memory value (plain MiB or the
// "current=<MiB>" property form).
func cfgMemoryMB(cfg map[string]any) (mb int64, present bool, err error) {
	if _, ok := cfg["memory"]; !ok {
		return 0, false, nil
	}
	s := cfgString(cfg, "memory")
	if mb = pveMemoryMB(s); mb < 1 {
		return 0, true, fmt.Errorf("memory=%q is not a valid size", s)
	}
	return mb, true, nil
}

// pveDefaultMemoryMB is PVE's memory for a VM or container whose config has no
// memory key.
const pveDefaultMemoryMB = 512

// guestVCPUTarget is the vCPU count PVE reports as maxcpu for a guest running
// `cores` cores: sockets × cores for a VM (sockets from cfg; PVE's default is 1
// when absent), cores for a container. A malformed sockets value is an error.
// A VM's vcpus hotplug cap is deliberately ignored: it can only LOWER maxcpu,
// so ignoring it over-counts — the safe direction for quota.
func guestVCPUTarget(guestType string, cfg map[string]any, cores int) (int, error) {
	if guestType != "qemu" {
		return cores, nil
	}
	sockets, present, err := cfgPositiveInt(cfg, "sockets")
	if err != nil {
		return 0, err
	}
	if !present {
		sockets = 1
	}
	return sockets * cores, nil
}

// errUnlimitedCores marks a container snapshot that sets no cores: such a
// container may use every host core, a size its config alone cannot tell us.
var errUnlimitedCores = errors.New("container snapshot sets no CPU limit")

// rollbackGrowthTarget reads the absolute vCPU and memory a guest will have
// after rolling back to the snapshot whose config is snapCfg. It fails closed:
// a present-but-malformed value is an error, never "unchanged". Absent keys
// take PVE's defaults (VM cores and sockets 1, memory 512 MiB) — except a
// container without cores, which has no CPU limit at all: errUnlimitedCores.
// Disk is not gated here: a rollback cannot make a disk larger than it is now.
func rollbackGrowthTarget(guestType string, snapCfg map[string]any) (vcpu int, ramMB int64, err error) {
	cores, present, err := cfgPositiveInt(snapCfg, "cores")
	if err != nil {
		return 0, 0, err
	}
	if !present {
		if guestType != "qemu" {
			return 0, 0, errUnlimitedCores
		}
		cores = 1
	}
	if vcpu, err = guestVCPUTarget(guestType, snapCfg, cores); err != nil {
		return 0, 0, err
	}
	ramMB, present, err = cfgMemoryMB(snapCfg)
	if err != nil {
		return 0, 0, err
	}
	if !present {
		ramMB = pveDefaultMemoryMB
	}
	return vcpu, ramMB, nil
}

// sizingUnreadable is the fail-closed verdict when sizing read back from
// Proxmox cannot be parsed: the change is refused because its quota charge
// cannot be verified.
func sizingUnreadable(what string, err error) *types.APIError {
	return &types.APIError{
		Code:    "proxmox_error",
		Message: fmt.Sprintf("The %s sizing could not be read (%v); the change was refused because its quota charge cannot be verified.", what, err),
		Status:  http.StatusBadGateway,
	}
}

func cfgString(cfg map[string]any, key string) string {
	switch v := cfg[key].(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		if v {
			return "1"
		}
		return "0"
	default:
		return ""
	}
}

// parsePVEValue splits "virtio=AA:BB:CC,bridge=vmbr0,tag=10" into the
// leading bare value (model=MAC style) and the key=value options.
func parsePVEValue(s string) (first string, opts map[string]string) {
	opts = map[string]string{}
	for i, part := range strings.Split(s, ",") {
		k, v, found := strings.Cut(part, "=")
		if !found {
			if i == 0 {
				first = part
			}
			continue
		}
		if i == 0 {
			first = part
		}
		opts[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return first, opts
}

func parseNICs(cfg map[string]any) []types.NICConfig {
	out := []types.NICConfig{}
	for key := range cfg {
		if !netKeyRe.MatchString(key) {
			continue
		}
		first, opts := parsePVEValue(cfgString(cfg, key))
		nic := types.NICConfig{Key: key, Bridge: opts["bridge"], IPConfig: opts["ip"]}
		// qemu: "virtio=AA:BB:.."; lxc: "name=eth0,...,hwaddr=AA:.."
		if model, mac, ok := strings.Cut(first, "="); ok && opts["name"] == "" {
			nic.Model = model
			nic.MAC = mac
		} else {
			nic.Model = opts["name"]
			nic.MAC = opts["hwaddr"]
		}
		if tag, err := strconv.Atoi(opts["tag"]); err == nil {
			nic.VLANTag = tag
		}
		nic.Firewall = opts["firewall"] == "1"
		out = append(out, nic)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

func parseDisks(cfg map[string]any) []types.DiskConfig {
	out := []types.DiskConfig{}
	for key := range cfg {
		if !diskKeyRe.MatchString(key) {
			continue
		}
		raw := cfgString(cfg, key)
		first, opts := parsePVEValue(raw)
		if first == "none" {
			continue
		}
		disk := types.DiskConfig{
			Key:       key,
			Volume:    first,
			Format:    opts["format"],
			SizeBytes: parsePVESize(opts["size"]),
			CDROM:     opts["media"] == "cdrom",
		}
		if storage, _, ok := strings.Cut(first, ":"); ok {
			disk.Storage = storage
		}
		out = append(out, disk)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// namedDiskSizeGiB returns the current provisioned size of ONE disk config key
// in whole GiB, floored — conservative for delta math: flooring the current
// size can only over-state a growth delta, never under-state it. Explicit
// errors (never invented values): a missing key, a CD-ROM drive, or a config
// entry without a parseable `size=` all reject the resize.
func namedDiskSizeGiB(cfg map[string]any, key string) (int64, *types.APIError) {
	raw := cfgString(cfg, key)
	if raw == "" || raw == "none" {
		return 0, &types.APIError{Code: "invalid_request", Message: fmt.Sprintf("disk %q not found on this guest", key), Status: http.StatusBadRequest}
	}
	_, opts := parsePVEValue(raw)
	if opts["media"] == "cdrom" {
		return 0, &types.APIError{Code: "invalid_request", Message: fmt.Sprintf("%q is a CD-ROM drive and cannot be resized", key), Status: http.StatusBadRequest}
	}
	bytes := parsePVESize(opts["size"])
	if bytes <= 0 {
		return 0, &types.APIError{Code: "invalid_request", Message: fmt.Sprintf("current size of disk %q is unavailable from Proxmox; try again", key), Status: http.StatusBadRequest}
	}
	return bytes >> 30, nil
}

// parsePVESize converts PVE size syntax ("32G", "512M", "1T", plain bytes)
// to bytes; unknown input yields 0 (unknown, never invented).
func parsePVESize(s string) int64 {
	if s == "" {
		return 0
	}
	mult := int64(1)
	switch s[len(s)-1] {
	case 'K':
		mult = 1 << 10
	case 'M':
		mult = 1 << 20
	case 'G':
		mult = 1 << 30
	case 'T':
		mult = 1 << 40
	}
	num := s
	if mult != 1 {
		num = s[:len(s)-1]
	}
	v, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return 0
	}
	return int64(v * float64(mult))
}
