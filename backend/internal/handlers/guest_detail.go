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

// enforceGrowth rejects a resize/config change that would push the caller's
// tenant/project past quota. `target` carries the ABSOLUTE requested size for
// the changed dimensions (0 = unchanged); the delta is target-minus-current
// floored at 0, so shrinks and no-ops pass. A snapshot miss on a real grow is a
// transient condition, rejected retryably (mirrors create's clone-source path).
// Quota is only enforced at create today, so this closes the grow-past-cap hole.
func (d *Deps) enforceGrowth(r *http.Request, ref proxmox.GuestRef, target store.Alloc) error {
	if target.VCPU <= 0 && target.RAMMB <= 0 && target.DiskGB <= 0 {
		return nil // no quota dimension is being set
	}
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
	delta := store.Alloc{
		VCPU:   max(0, target.VCPU-cur.VCPU),
		RAMMB:  max(0, target.RAMMB-cur.RAMMB),
		DiskGB: max(0, target.DiskGB-cur.DiskGB),
	}
	if delta.VCPU == 0 && delta.RAMMB == 0 && delta.DiskGB == 0 {
		return nil // requested size ≤ current: not a grow
	}
	if err := d.Store.CheckGuestGrowth(r.Context(), store.GrowthCheckParams{
		TenantID: id.ActiveTenantID, ProjectID: id.ResolvedProjectID, Snapshot: snap, Delta: delta,
	}); err != nil {
		var qe store.ErrQuotaExceeded
		if errors.As(err, &qe) {
			return &types.APIError{Code: "quota_exceeded", Message: quotaExceededMessage(qe), Status: http.StatusConflict}
		}
		d.logger().Error("growth quota check", "vmid", ref.VMID, "err", err)
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
	target := store.Alloc{}
	if req.Cores != nil {
		target.VCPU = *req.Cores
	}
	if req.MemoryMB != nil {
		target.RAMMB = *req.MemoryMB
	}
	if err := d.enforceGrowth(r, ref, target); err != nil {
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
	d.trackRes(upid, label, "resizing", types.TaskResource{Type: ref.Type, VMID: ref.VMID, Node: ref.Node})
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

	// Enforce quota on the disk grow (target size vs the guest's current total).
	if err := d.enforceGrowth(r, ref, store.Alloc{DiskGB: int64(req.SizeGiB)}); err != nil {
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
		d.trackRes(upid, label, "resizing", types.TaskResource{Type: ref.Type, VMID: ref.VMID, Node: ref.Node})
	}
	httpserver.WriteJSON(w, http.StatusAccepted, types.TaskRef{UPID: string(upid), Action: label})
}

// ── config-string parsing ────────────────────────────────────────────────────

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
