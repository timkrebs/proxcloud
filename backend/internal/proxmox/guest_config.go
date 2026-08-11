package proxmox

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
)

// ErrAgentUnavailable marks the honest "QEMU guest agent not running /
// not configured" state — a condition the UI renders, not a failure.
var ErrAgentUnavailable = errors.New("proxmox: guest agent unavailable")

func unixTime(sec int64) time.Time { return time.Unix(sec, 0).UTC() }

// GuestConfig implements Client: the raw config map (current values, not
// pending) from /nodes/{n}/{type}/{vmid}/config.
func (g *GoPVE) GuestConfig(ctx context.Context, ref GuestRef) (map[string]any, error) {
	ctx, cancel := readCtx(ctx)
	defer cancel()

	var cfg map[string]any
	if err := g.c.Get(ctx, ref.path()+"/config", &cfg); err != nil {
		return nil, mapErr(fmt.Sprintf("query config of %s/%d", ref.Type, ref.VMID), err)
	}
	return cfg, nil
}

// SetGuestConfig implements Client. qemu config changes are async (POST →
// UPID); lxc changes are synchronous (PUT → empty UPID).
func (g *GoPVE) SetGuestConfig(ctx context.Context, ref GuestRef, changes map[string]any) (UPID, error) {
	ctx, cancel := mutationCtx(ctx)
	defer cancel()

	if ref.Type == "qemu" {
		var upid upidResult
		if err := g.c.Post(ctx, ref.path()+"/config", changes, &upid); err != nil {
			return "", mapErr(fmt.Sprintf("update config of %s/%d", ref.Type, ref.VMID), err)
		}
		return UPID(upid), nil
	}
	var ignored any
	if err := g.c.Put(ctx, ref.path()+"/config", changes, &ignored); err != nil {
		return "", mapErr(fmt.Sprintf("update config of %s/%d", ref.Type, ref.VMID), err)
	}
	return "", nil
}

// GuestRRD implements Client — same shape as NodeRRD for guest series.
// Keys: cpu, maxcpu (percent 0-100 for cpu), mem, maxmem (bytes),
// netin, netout, diskread, diskwrite (bytes/s rates from PVE).
func (g *GoPVE) GuestRRD(ctx context.Context, ref GuestRef, timeframe string) (map[string][]types.MetricPoint, error) {
	if timeframe == "" {
		timeframe = "hour"
	}
	ctx, cancel := readCtx(ctx)
	defer cancel()

	var rows []map[string]*float64
	p := fmt.Sprintf("%s/rrddata?timeframe=%s&cf=AVERAGE", ref.path(), url.QueryEscape(timeframe))
	if err := g.c.Get(ctx, p, &rows); err != nil {
		return nil, mapErr(fmt.Sprintf("query metrics of %s/%d", ref.Type, ref.VMID), err)
	}

	out := map[string][]types.MetricPoint{}
	for _, row := range rows {
		t, ok := row["time"]
		if !ok || t == nil {
			continue
		}
		ts := unixTime(int64(*t))
		for key, v := range row {
			if key == "time" || v == nil {
				continue
			}
			val := *v
			if key == "cpu" {
				val *= 100 // fraction → percent, matching the wire contract
			}
			out[key] = append(out[key], types.MetricPoint{T: ts, V: val})
		}
	}
	for key := range out {
		s := out[key]
		sort.Slice(s, func(i, j int) bool { return s[i].T.Before(s[j].T) })
	}
	return out, nil
}

// AgentInterfaces implements Client: live IPs from the QEMU guest agent or
// the LXC interfaces endpoint. ErrAgentUnavailable marks the honest
// "agent not running" state for qemu guests.
func (g *GoPVE) AgentInterfaces(ctx context.Context, ref GuestRef) ([]types.GuestNIC, error) {
	ctx, cancel := readCtx(ctx)
	defer cancel()

	if ref.Type == "lxc" {
		var rows []struct {
			Name  string `json:"name"`
			MAC   string `json:"hwaddr"`
			Inet  string `json:"inet"`
			Inet6 string `json:"inet6"`
		}
		if err := g.c.Get(ctx, ref.path()+"/interfaces", &rows); err != nil {
			return nil, mapErr(fmt.Sprintf("query interfaces of lxc/%d", ref.VMID), err)
		}
		out := make([]types.GuestNIC, 0, len(rows))
		for _, r := range rows {
			nic := types.GuestNIC{Name: r.Name, MAC: r.MAC, IPv4: []string{}, IPv6: []string{}}
			if r.Inet != "" {
				nic.IPv4 = append(nic.IPv4, r.Inet)
			}
			if r.Inet6 != "" {
				nic.IPv6 = append(nic.IPv6, r.Inet6)
			}
			out = append(out, nic)
		}
		return out, nil
	}

	var res struct {
		Result []struct {
			Name string `json:"name"`
			MAC  string `json:"hardware-address"`
			IPs  []struct {
				Type    string `json:"ip-address-type"`
				Address string `json:"ip-address"`
				Prefix  int    `json:"prefix"`
			} `json:"ip-addresses"`
		} `json:"result"`
	}
	if err := g.c.Get(ctx, ref.path()+"/agent/network-get-interfaces", &res); err != nil {
		mapped := mapErr(fmt.Sprintf("query agent interfaces of qemu/%d", ref.VMID), err)
		// PVE answers 500 "QEMU guest agent is not running" (or "No QEMU
		// guest agent configured") — a state, not a failure.
		if apiErr, ok := mapped.(*types.APIError); ok &&
			strings.Contains(strings.ToLower(apiErr.PVEMessage+apiErr.Message), "guest agent") {
			return nil, ErrAgentUnavailable
		}
		return nil, mapped
	}
	out := make([]types.GuestNIC, 0, len(res.Result))
	for _, r := range res.Result {
		if r.Name == "lo" {
			continue
		}
		nic := types.GuestNIC{Name: r.Name, MAC: r.MAC, IPv4: []string{}, IPv6: []string{}}
		for _, ip := range r.IPs {
			addr := fmt.Sprintf("%s/%d", ip.Address, ip.Prefix)
			if ip.Type == "ipv6" {
				nic.IPv6 = append(nic.IPv6, addr)
			} else {
				nic.IPv4 = append(nic.IPv4, addr)
			}
		}
		out = append(out, nic)
	}
	return out, nil
}

// ResizeDisk implements Client. size is PVE syntax, e.g. "64G" (absolute).
func (g *GoPVE) ResizeDisk(ctx context.Context, ref GuestRef, disk, size string) (UPID, error) {
	ctx, cancel := mutationCtx(ctx)
	defer cancel()

	var upid upidResult
	body := map[string]any{"disk": disk, "size": size}
	if err := g.c.Put(ctx, ref.path()+"/resize", body, &upid); err != nil {
		return "", mapErr(fmt.Sprintf("resize %s of %s/%d", disk, ref.Type, ref.VMID), err)
	}
	return UPID(upid), nil
}

// Snapshots implements Client.
func (g *GoPVE) Snapshots(ctx context.Context, ref GuestRef) ([]types.Snapshot, error) {
	ctx, cancel := readCtx(ctx)
	defer cancel()

	var rows []struct {
		Name        string    `json:"name"`
		Description string    `json:"description"`
		Parent      string    `json:"parent"`
		SnapTime    flexInt64 `json:"snaptime"`
		VMState     flexInt64 `json:"vmstate"`
	}
	if err := g.c.Get(ctx, ref.path()+"/snapshot", &rows); err != nil {
		return nil, mapErr(fmt.Sprintf("query snapshots of %s/%d", ref.Type, ref.VMID), err)
	}
	out := []types.Snapshot{}
	for _, r := range rows {
		if r.Name == "current" {
			// PVE's synthetic "current" row marks the live state, not a
			// snapshot; represented via Current on the parent instead.
			continue
		}
		out = append(out, types.Snapshot{
			Name:        r.Name,
			Description: strings.TrimSpace(r.Description),
			Parent:      r.Parent,
			SnapTime:    unixTime(int64(r.SnapTime)),
			VMState:     r.VMState == 1,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SnapTime.After(out[j].SnapTime) })
	return out, nil
}

// CreateSnapshot implements Client.
func (g *GoPVE) CreateSnapshot(ctx context.Context, ref GuestRef, name, desc string, vmstate bool) (UPID, error) {
	ctx, cancel := mutationCtx(ctx)
	defer cancel()

	body := map[string]any{"snapname": name}
	if desc != "" {
		body["description"] = desc
	}
	if vmstate && ref.Type == "qemu" {
		body["vmstate"] = 1
	}
	var upid upidResult
	if err := g.c.Post(ctx, ref.path()+"/snapshot", body, &upid); err != nil {
		return "", mapErr(fmt.Sprintf("create snapshot of %s/%d", ref.Type, ref.VMID), err)
	}
	return UPID(upid), nil
}

// RollbackSnapshot implements Client.
func (g *GoPVE) RollbackSnapshot(ctx context.Context, ref GuestRef, name string) (UPID, error) {
	ctx, cancel := mutationCtx(ctx)
	defer cancel()

	var upid upidResult
	p := fmt.Sprintf("%s/snapshot/%s/rollback", ref.path(), url.PathEscape(name))
	if err := g.c.Post(ctx, p, map[string]any{}, &upid); err != nil {
		return "", mapErr(fmt.Sprintf("roll back snapshot %s of %s/%d", name, ref.Type, ref.VMID), err)
	}
	return UPID(upid), nil
}

// DeleteSnapshot implements Client.
func (g *GoPVE) DeleteSnapshot(ctx context.Context, ref GuestRef, name string) (UPID, error) {
	ctx, cancel := mutationCtx(ctx)
	defer cancel()

	var upid upidResult
	p := fmt.Sprintf("%s/snapshot/%s", ref.path(), url.PathEscape(name))
	if err := g.c.Delete(ctx, p, &upid); err != nil {
		return "", mapErr(fmt.Sprintf("delete snapshot %s of %s/%d", name, ref.Type, ref.VMID), err)
	}
	return UPID(upid), nil
}

// FirewallRules implements Client: options + rules in one document.
func (g *GoPVE) FirewallRules(ctx context.Context, ref GuestRef) (*types.GuestFirewall, error) {
	ctx, cancel := readCtx(ctx)
	defer cancel()

	var opts struct {
		Enable flexInt64 `json:"enable"`
	}
	if err := g.c.Get(ctx, ref.path()+"/firewall/options", &opts); err != nil {
		return nil, mapErr(fmt.Sprintf("query firewall options of %s/%d", ref.Type, ref.VMID), err)
	}
	var rows []struct {
		Pos     flexInt64 `json:"pos"`
		Enable  flexInt64 `json:"enable"`
		Type    string    `json:"type"`
		Action  string    `json:"action"`
		Source  string    `json:"source"`
		Dest    string    `json:"dest"`
		Proto   string    `json:"proto"`
		DPort   string    `json:"dport"`
		SPort   string    `json:"sport"`
		Comment string    `json:"comment"`
	}
	if err := g.c.Get(ctx, ref.path()+"/firewall/rules", &rows); err != nil {
		return nil, mapErr(fmt.Sprintf("query firewall rules of %s/%d", ref.Type, ref.VMID), err)
	}

	fw := &types.GuestFirewall{Enabled: opts.Enable == 1, Rules: []types.FirewallRule{}}
	for _, r := range rows {
		fw.Rules = append(fw.Rules, types.FirewallRule{
			Pos:     int(r.Pos),
			Enable:  r.Enable == 1,
			Type:    r.Type,
			Action:  r.Action,
			Source:  r.Source,
			Dest:    r.Dest,
			Proto:   r.Proto,
			DPort:   r.DPort,
			SPort:   r.SPort,
			Comment: r.Comment,
		})
	}
	return fw, nil
}

// SetFirewallEnabled implements Client.
func (g *GoPVE) SetFirewallEnabled(ctx context.Context, ref GuestRef, on bool) error {
	ctx, cancel := mutationCtx(ctx)
	defer cancel()

	enable := 0
	if on {
		enable = 1
	}
	var ignored any
	if err := g.c.Put(ctx, ref.path()+"/firewall/options", map[string]any{"enable": enable}, &ignored); err != nil {
		return mapErr(fmt.Sprintf("toggle firewall of %s/%d", ref.Type, ref.VMID), err)
	}
	return nil
}

// ACL implements Client: every ACL entry (the handler filters per guest).
func (g *GoPVE) ACL(ctx context.Context) ([]types.ACLEntry, error) {
	ctx, cancel := readCtx(ctx)
	defer cancel()

	var rows []struct {
		Path      string    `json:"path"`
		UGID      string    `json:"ugid"`
		Type      string    `json:"type"`
		RoleID    string    `json:"roleid"`
		Propagate flexInt64 `json:"propagate"`
	}
	if err := g.c.Get(ctx, "/access/acl", &rows); err != nil {
		return nil, mapErr("query access control list", err)
	}
	out := make([]types.ACLEntry, 0, len(rows))
	for _, r := range rows {
		out = append(out, types.ACLEntry{
			Path: r.Path, UGID: r.UGID, Type: r.Type, Role: r.RoleID, Propagate: r.Propagate == 1,
		})
	}
	return out, nil
}
