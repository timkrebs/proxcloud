package proxmox

import (
	"context"
	"fmt"
	"net/url"
	"sort"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
)

func mutationCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, mutationTimeout)
}

func (r GuestRef) path() string {
	return fmt.Sprintf("/nodes/%s/%s/%d", url.PathEscape(r.Node), r.Type, r.VMID)
}

// guestStatusWire tolerates PVE's loose numerics (see rawResourceWire).
type guestStatusWire struct {
	Status    string    `json:"status"`
	Name      string    `json:"name"`
	Uptime    int64     `json:"uptime"`
	CPU       float64   `json:"cpu"`
	CPUs      float64   `json:"cpus"`
	Mem       flexInt64 `json:"mem"`
	MaxMem    flexInt64 `json:"maxmem"`
	DiskRead  flexInt64 `json:"diskread"`
	DiskWrite flexInt64 `json:"diskwrite"`
	NetIn     flexInt64 `json:"netin"`
	NetOut    flexInt64 `json:"netout"`
	Agent     flexInt64 `json:"agent"`
}

// GuestStatus implements Client.
func (g *GoPVE) GuestStatus(ctx context.Context, ref GuestRef) (*GuestStatusInfo, error) {
	ctx, cancel := readCtx(ctx)
	defer cancel()

	var w guestStatusWire
	if err := g.c.Get(ctx, ref.path()+"/status/current", &w); err != nil {
		return nil, mapErr(fmt.Sprintf("query status of %s/%d", ref.Type, ref.VMID), err)
	}
	return &GuestStatusInfo{
		Status:    w.Status,
		Name:      w.Name,
		UptimeSec: w.Uptime,
		CPUPct:    w.CPU * 100,
		Cores:     int(w.CPUs),
		MemUsed:   int64(w.Mem),
		MemMax:    int64(w.MaxMem),
		DiskRead:  int64(w.DiskRead),
		DiskWrite: int64(w.DiskWrite),
		NetIn:     int64(w.NetIn),
		NetOut:    int64(w.NetOut),
		Agent:     ref.Type == "qemu" && w.Agent == 1,
	}, nil
}

// upidResult decodes the bare-UPID responses PVE returns for async calls.
type upidResult string

// GuestAction implements Client.
func (g *GoPVE) GuestAction(ctx context.Context, ref GuestRef, action string) (UPID, error) {
	switch action {
	case "start", "stop", "shutdown", "reboot", "reset":
	default:
		return "", &types.APIError{Code: "invalid_request", Message: fmt.Sprintf("unknown guest action %q", action), Status: 400}
	}
	ctx, cancel := mutationCtx(ctx)
	defer cancel()

	var upid upidResult
	if err := g.c.Post(ctx, ref.path()+"/status/"+action, map[string]any{}, &upid); err != nil {
		return "", mapErr(fmt.Sprintf("%s %s/%d", action, ref.Type, ref.VMID), err)
	}
	return UPID(upid), nil
}

// DeleteGuest implements Client.
func (g *GoPVE) DeleteGuest(ctx context.Context, ref GuestRef, purge bool) (UPID, error) {
	ctx, cancel := mutationCtx(ctx)
	defer cancel()

	p := ref.path()
	if purge {
		p += "?purge=1&destroy-unreferenced-disks=1"
	}
	var upid upidResult
	if err := g.c.Delete(ctx, p, &upid); err != nil {
		return "", mapErr(fmt.Sprintf("delete %s/%d", ref.Type, ref.VMID), err)
	}
	return UPID(upid), nil
}

// taskWire is one row of /cluster/tasks or a task status body.
type taskWire struct {
	UPID       string    `json:"upid"`
	Node       string    `json:"node"`
	Type       string    `json:"type"`
	ID         string    `json:"id"`
	User       string    `json:"user"`
	Status     string    `json:"status"` // status body: "running" | "stopped"
	ExitStatus string    `json:"exitstatus"`
	StartTime  flexInt64 `json:"starttime"`
	EndTime    flexInt64 `json:"endtime"`
}

func (w taskWire) info() TaskInfo {
	info := TaskInfo{
		UPID:       UPID(w.UPID),
		Node:       w.Node,
		Type:       w.Type,
		ID:         w.ID,
		User:       w.User,
		StartTime:  int64(w.StartTime),
		EndTime:    int64(w.EndTime),
		ExitStatus: w.ExitStatus,
	}
	// /cluster/tasks rows carry the exit status in "status" ("OK" or the
	// error text); the per-task status endpoint uses "exitstatus" and keeps
	// "status" for running|stopped. Normalize to ExitStatus either way.
	if info.ExitStatus == "" && w.Status != "" && w.Status != "running" && w.Status != "stopped" {
		info.ExitStatus = w.Status
	}
	return info
}

// ClusterTasks implements Client.
func (g *GoPVE) ClusterTasks(ctx context.Context) ([]TaskInfo, error) {
	ctx, cancel := readCtx(ctx)
	defer cancel()

	var rows []taskWire
	if err := g.c.Get(ctx, "/cluster/tasks", &rows); err != nil {
		return nil, mapErr("query cluster tasks", err)
	}
	out := make([]TaskInfo, 0, len(rows))
	for _, w := range rows {
		out = append(out, w.info())
	}
	// Newest first, matching the activity-log presentation.
	sort.Slice(out, func(i, j int) bool { return out[i].StartTime > out[j].StartTime })
	return out, nil
}

// TaskStatus implements Client.
func (g *GoPVE) TaskStatus(ctx context.Context, upid UPID) (*TaskInfo, error) {
	node := upid.Node()
	if node == "" {
		return nil, &types.APIError{Code: "invalid_request", Message: fmt.Sprintf("malformed UPID %q", upid), Status: 400}
	}
	ctx, cancel := readCtx(ctx)
	defer cancel()

	var w taskWire
	p := fmt.Sprintf("/nodes/%s/tasks/%s/status", url.PathEscape(node), url.PathEscape(string(upid)))
	if err := g.c.Get(ctx, p, &w); err != nil {
		return nil, mapErr("query task status", err)
	}
	info := w.info()
	// The status body reports "running"/"stopped" instead of an endtime for
	// running tasks; normalize so EndTime==0 <=> running holds everywhere.
	if w.Status == "running" {
		info.EndTime = 0
		info.ExitStatus = ""
	} else if info.EndTime == 0 {
		info.EndTime = info.StartTime
	}
	return &info, nil
}

// TaskLog implements Client.
func (g *GoPVE) TaskLog(ctx context.Context, upid UPID, start, limit int) ([]types.TaskLogLine, int, error) {
	node := upid.Node()
	if node == "" {
		return nil, 0, &types.APIError{Code: "invalid_request", Message: fmt.Sprintf("malformed UPID %q", upid), Status: 400}
	}
	ctx, cancel := readCtx(ctx)
	defer cancel()

	if limit <= 0 {
		limit = 200
	}
	var rows []struct {
		N int    `json:"n"`
		T string `json:"t"`
	}
	p := fmt.Sprintf("/nodes/%s/tasks/%s/log?start=%d&limit=%d",
		url.PathEscape(node), url.PathEscape(string(upid)), start, limit)
	if err := g.c.Get(ctx, p, &rows); err != nil {
		return nil, 0, mapErr("query task log", err)
	}
	lines := make([]types.TaskLogLine, 0, len(rows))
	for _, r := range rows {
		lines = append(lines, types.TaskLogLine{N: r.N, T: r.T})
	}
	// PVE reports the total via the response's "total" attribute which the
	// generic data unwrap drops; derive a usable total from what we know.
	total := start + len(lines)
	if len(lines) == limit {
		total++ // signal "more available" without fabricating an exact count
	}
	return lines, total, nil
}
