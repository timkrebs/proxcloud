package proxmox

import (
	"context"
	"crypto/tls"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	goproxmox "github.com/luthermonson/go-proxmox"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/config"
)

// Per-call context deadlines, applied inside every method so callers cannot
// forget them. Mutations get longer because PVE serializes config locks.
const (
	readTimeout     = 10 * time.Second
	mutationTimeout = 30 * time.Second // used by the mutation methods of later milestones
)

// GoPVE is the production Client backed by github.com/luthermonson/go-proxmox.
// Thin endpoints (rrddata, node storage listing, network bridges, storage
// content) go through the library's raw Get with our own wire structs.
type GoPVE struct {
	c *goproxmox.Client
}

var _ Client = (*GoPVE)(nil)

// New builds the production client from config: API-token auth via the
// "PVEAPIToken=<id>=<secret>" header, TLS verification skipped only when
// ProxmoxTLSInsecure is set (homelab self-signed certs).
func New(cfg *config.Config) (*GoPVE, error) {
	u, err := url.Parse(cfg.ProxmoxURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("proxmox: PROXMOX_URL must be an absolute URL like https://host:8006, got %q", cfg.ProxmoxURL)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("proxmox: PROXMOX_URL scheme must be http or https, got %q", u.Scheme)
	}
	if cfg.ProxmoxTokenID == "" || cfg.ProxmoxTokenSecret == "" {
		return nil, fmt.Errorf("proxmox: PROXMOX_TOKEN_ID and PROXMOX_TOKEN_SECRET are required")
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	if cfg.ProxmoxTLSInsecure {
		if transport.TLSClientConfig == nil {
			transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
		}
		transport.TLSClientConfig.InsecureSkipVerify = true
	}

	base := strings.TrimSuffix(strings.TrimRight(u.String(), "/"), "/api2/json") + "/api2/json"
	c := goproxmox.NewClient(base,
		goproxmox.WithHTTPClient(&http.Client{Transport: transport}),
		goproxmox.WithAPIToken(cfg.ProxmoxTokenID, cfg.ProxmoxTokenSecret),
		goproxmox.WithUserAgent("proxcloud"),
	)
	return &GoPVE{c: c}, nil
}

func readCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, readTimeout)
}

// Version implements Client.
func (g *GoPVE) Version(ctx context.Context) (string, error) {
	ctx, cancel := readCtx(ctx)
	defer cancel()

	var v struct {
		Version string `json:"version"`
		Release string `json:"release"`
	}
	if err := g.c.Get(ctx, "/version", &v); err != nil {
		return "", mapErr("query Proxmox version", err)
	}
	return v.Version, nil
}

// ClusterStatus implements Client.
func (g *GoPVE) ClusterStatus(ctx context.Context) (*ClusterInfo, error) {
	ctx, cancel := readCtx(ctx)
	defer cancel()

	var rows []struct {
		Type    string `json:"type"` // cluster | node
		Name    string `json:"name"`
		Quorate int    `json:"quorate"`
		Online  int    `json:"online"`
	}
	if err := g.c.Get(ctx, "/cluster/status", &rows); err != nil {
		return nil, mapErr("query cluster status", err)
	}

	info := &ClusterInfo{}
	clustered := false
	for _, r := range rows {
		switch r.Type {
		case "cluster":
			clustered = true
			info.Name = r.Name
			info.Quorate = r.Quorate == 1
		case "node":
			info.NodesTotal++
			if r.Online == 1 {
				info.NodesOnline++
			}
		}
	}
	// A standalone node has no "cluster" row and no corosync quorum concept:
	// it is trivially its own quorum while online.
	if !clustered {
		info.Quorate = info.NodesOnline > 0
	}
	return info, nil
}

// rawResourceWire mirrors RawResource with json tags and lenient numeric
// types: byte counters can arrive in scientific notation above ~1PB, and
// maxcpu can be fractional for cpulimit'ed guests.
type rawResourceWire struct {
	ID         string    `json:"id"`
	Type       string    `json:"type"`
	Node       string    `json:"node"`
	Name       string    `json:"name"`
	Status     string    `json:"status"`
	Pool       string    `json:"pool"`
	Tags       string    `json:"tags"`
	VMID       int       `json:"vmid"`
	Template   int       `json:"template"`
	CPU        float64   `json:"cpu"`
	MaxCPU     float64   `json:"maxcpu"`
	Mem        flexInt64 `json:"mem"`
	MaxMem     flexInt64 `json:"maxmem"`
	Disk       flexInt64 `json:"disk"`
	MaxDisk    flexInt64 `json:"maxdisk"`
	Uptime     int64     `json:"uptime"`
	Storage    string    `json:"storage"`
	PluginType string    `json:"plugintype"`
	Content    string    `json:"content"`
	Shared     int       `json:"shared"`
	HAState    string    `json:"hastate"`
}

// ClusterResources implements Client.
func (g *GoPVE) ClusterResources(ctx context.Context) ([]RawResource, error) {
	ctx, cancel := readCtx(ctx)
	defer cancel()

	var rows []rawResourceWire
	if err := g.c.Get(ctx, "/cluster/resources", &rows); err != nil {
		return nil, mapErr("query cluster resources", err)
	}

	out := make([]RawResource, 0, len(rows))
	for _, r := range rows {
		out = append(out, RawResource{
			ID:         r.ID,
			Type:       r.Type,
			Node:       r.Node,
			Name:       r.Name,
			Status:     r.Status,
			Pool:       r.Pool,
			Tags:       r.Tags,
			VMID:       r.VMID,
			Template:   r.Template == 1,
			CPU:        r.CPU,
			MaxCPU:     int(r.MaxCPU),
			Mem:        int64(r.Mem),
			MaxMem:     int64(r.MaxMem),
			Disk:       int64(r.Disk),
			MaxDisk:    int64(r.MaxDisk),
			Uptime:     r.Uptime,
			Storage:    r.Storage,
			PluginType: r.PluginType,
			Content:    r.Content,
			Shared:     r.Shared == 1,
			HAState:    r.HAState,
		})
	}
	return out, nil
}

// NextID implements Client. PVE returns the id as a JSON string ("100");
// tolerate a plain number too.
func (g *GoPVE) NextID(ctx context.Context) (int, error) {
	ctx, cancel := readCtx(ctx)
	defer cancel()

	var raw any
	if err := g.c.Get(ctx, "/cluster/nextid", &raw); err != nil {
		return 0, mapErr("query next free VMID", err)
	}
	switch v := raw.(type) {
	case string:
		id, err := strconv.Atoi(v)
		if err != nil {
			return 0, mapErr("parse next free VMID", fmt.Errorf("unexpected /cluster/nextid value %q", v))
		}
		return id, nil
	case float64:
		return int(v), nil
	default:
		return 0, mapErr("parse next free VMID", fmt.Errorf("unexpected /cluster/nextid value of type %T", raw))
	}
}

// Pools implements Client. The /pools index carries no members, so each
// pool's count comes from the (non-deprecated) query-parameter detail form.
func (g *GoPVE) Pools(ctx context.Context) ([]types.Pool, error) {
	ctx, cancel := readCtx(ctx)
	defer cancel()

	var rows []struct {
		PoolID  string `json:"poolid"`
		Comment string `json:"comment"`
	}
	if err := g.c.Get(ctx, "/pools", &rows); err != nil {
		return nil, mapErr("query pools", err)
	}

	pools := make([]types.Pool, 0, len(rows))
	for _, r := range rows {
		var detail []struct {
			Members []map[string]any `json:"members"`
		}
		if err := g.c.Get(ctx, "/pools?poolid="+url.QueryEscape(r.PoolID), &detail); err != nil {
			return nil, mapErr("query pool "+r.PoolID, err)
		}
		members := 0
		if len(detail) > 0 {
			members = len(detail[0].Members)
		}
		pools = append(pools, types.Pool{PoolID: r.PoolID, Comment: r.Comment, Members: members})
	}
	sort.Slice(pools, func(i, j int) bool { return pools[i].PoolID < pools[j].PoolID })
	return pools, nil
}

// NodeStatus implements Client.
func (g *GoPVE) NodeStatus(ctx context.Context, node string) (*NodeStatusInfo, error) {
	ctx, cancel := readCtx(ctx)
	defer cancel()

	type memStat struct {
		Used  flexInt64 `json:"used"`
		Total flexInt64 `json:"total"`
	}
	var st struct {
		Uptime     int64   `json:"uptime"`
		CPU        float64 `json:"cpu"` // fraction 0-1
		LoadAvg    []any   `json:"loadavg"`
		Kversion   string  `json:"kversion"`
		PVEVersion string  `json:"pveversion"`
		CPUInfo    struct {
			Model   string `json:"model"`
			Cores   int    `json:"cores"`
			Sockets int    `json:"sockets"`
		} `json:"cpuinfo"`
		Memory memStat `json:"memory"`
		Swap   memStat `json:"swap"`
		RootFS memStat `json:"rootfs"`
	}
	if err := g.c.Get(ctx, "/nodes/"+url.PathEscape(node)+"/status", &st); err != nil {
		return nil, mapErr("query status of node "+node, err)
	}

	// PVE reports loadavg as an array of strings; tolerate numbers too.
	loads := make([]float64, 0, len(st.LoadAvg))
	for _, v := range st.LoadAvg {
		switch x := v.(type) {
		case string:
			if f, err := strconv.ParseFloat(x, 64); err == nil {
				loads = append(loads, f)
			}
		case float64:
			loads = append(loads, x)
		}
	}

	return &NodeStatusInfo{
		Uptime:        st.Uptime,
		CPUPct:        st.CPU * 100,
		LoadAvg:       loads,
		KernelVersion: st.Kversion,
		CPUModel:      st.CPUInfo.Model,
		CPUCores:      st.CPUInfo.Cores,
		CPUSockets:    st.CPUInfo.Sockets,
		MemUsed:       int64(st.Memory.Used),
		MemTotal:      int64(st.Memory.Total),
		SwapUsed:      int64(st.Swap.Used),
		SwapTotal:     int64(st.Swap.Total),
		RootFSUsed:    int64(st.RootFS.Used),
		RootFSTotal:   int64(st.RootFS.Total),
		PVEVersion:    st.PVEVersion,
	}, nil
}

var validTimeframes = map[string]bool{
	"hour": true, "day": true, "week": true, "month": true, "year": true,
}

// NodeRRD implements Client.
func (g *GoPVE) NodeRRD(ctx context.Context, node, timeframe string) (map[string][]types.MetricPoint, error) {
	if timeframe == "" {
		timeframe = "hour"
	}
	if !validTimeframes[timeframe] {
		return nil, &types.APIError{
			Code:    "invalid_request",
			Message: fmt.Sprintf("invalid timeframe %q (want hour|day|week|month|year)", timeframe),
			Status:  http.StatusBadRequest,
		}
	}

	ctx, cancel := readCtx(ctx)
	defer cancel()

	// Pointers keep PVE's nulls distinguishable from real zeroes so gaps in
	// the RRD are dropped instead of fabricated as 0.
	var rows []struct {
		Time     int64    `json:"time"`
		CPU      *float64 `json:"cpu"`
		IOWait   *float64 `json:"iowait"`
		MemUsed  *float64 `json:"memused"`
		MemTotal *float64 `json:"memtotal"`
		NetIn    *float64 `json:"netin"`
		NetOut   *float64 `json:"netout"`
	}
	p := "/nodes/" + url.PathEscape(node) + "/rrddata?timeframe=" + url.QueryEscape(timeframe) + "&cf=AVERAGE"
	if err := g.c.Get(ctx, p, &rows); err != nil {
		return nil, mapErr("query metrics of node "+node, err)
	}

	series := map[string][]types.MetricPoint{
		"cpu":      {},
		"iowait":   {},
		"memused":  {},
		"memtotal": {},
		"netin":    {},
		"netout":   {},
	}
	for _, r := range rows {
		t := time.Unix(r.Time, 0).UTC()
		addPoint(series, "cpu", t, r.CPU, 100) // PVE fraction 0-1 -> percent
		addPoint(series, "iowait", t, r.IOWait, 100)
		addPoint(series, "memused", t, r.MemUsed, 1)
		addPoint(series, "memtotal", t, r.MemTotal, 1)
		addPoint(series, "netin", t, r.NetIn, 1)
		addPoint(series, "netout", t, r.NetOut, 1)
	}
	return series, nil
}

// addPoint appends one sample when it is present and finite; absent samples
// (RRD gaps, node reboots) are dropped honestly.
func addPoint(series map[string][]types.MetricPoint, key string, t time.Time, v *float64, mul float64) {
	if v == nil || math.IsNaN(*v) || math.IsInf(*v, 0) {
		return
	}
	series[key] = append(series[key], types.MetricPoint{T: t, V: *v * mul})
}

// NodeBridges implements Client. type=any_bridge covers Linux and OVS bridges.
func (g *GoPVE) NodeBridges(ctx context.Context, node string) ([]types.Bridge, error) {
	ctx, cancel := readCtx(ctx)
	defer cancel()

	var rows []struct {
		Iface    string `json:"iface"`
		Active   int    `json:"active"`
		Comments string `json:"comments"`
	}
	p := "/nodes/" + url.PathEscape(node) + "/network?type=any_bridge"
	if err := g.c.Get(ctx, p, &rows); err != nil {
		return nil, mapErr("query bridges of node "+node, err)
	}

	bridges := make([]types.Bridge, 0, len(rows))
	for _, r := range rows {
		bridges = append(bridges, types.Bridge{
			Iface:   r.Iface,
			Active:  r.Active == 1,
			Comment: strings.TrimSpace(r.Comments),
		})
	}
	sort.Slice(bridges, func(i, j int) bool { return bridges[i].Iface < bridges[j].Iface })
	return bridges, nil
}

// NodeStorages implements Client.
func (g *GoPVE) NodeStorages(ctx context.Context, node, content string) ([]types.NodeStorage, error) {
	ctx, cancel := readCtx(ctx)
	defer cancel()

	var rows []struct {
		Storage string    `json:"storage"`
		Type    string    `json:"type"`
		Content string    `json:"content"`
		Active  int       `json:"active"`
		Enabled int       `json:"enabled"`
		Shared  int       `json:"shared"`
		Used    flexInt64 `json:"used"`
		Total   flexInt64 `json:"total"`
		Avail   flexInt64 `json:"avail"`
	}
	p := "/nodes/" + url.PathEscape(node) + "/storage"
	if content != "" {
		p += "?content=" + url.QueryEscape(content)
	}
	if err := g.c.Get(ctx, p, &rows); err != nil {
		return nil, mapErr("query storages of node "+node, err)
	}

	out := make([]types.NodeStorage, 0, len(rows))
	for _, r := range rows {
		out = append(out, types.NodeStorage{
			Storage: r.Storage,
			Type:    r.Type,
			Content: splitList(r.Content, ","),
			Active:  r.Active == 1,
			Enabled: r.Enabled == 1,
			Shared:  r.Shared == 1,
			Used:    int64(r.Used),
			Total:   int64(r.Total),
			Avail:   int64(r.Avail),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Storage < out[j].Storage })
	return out, nil
}

// StorageContent implements Client.
func (g *GoPVE) StorageContent(ctx context.Context, node, storage, content string) ([]types.StorageContentItem, error) {
	ctx, cancel := readCtx(ctx)
	defer cancel()

	var rows []struct {
		VolID  string    `json:"volid"`
		Format string    `json:"format"`
		Size   flexInt64 `json:"size"`
		Notes  string    `json:"notes"`
	}
	p := "/nodes/" + url.PathEscape(node) + "/storage/" + url.PathEscape(storage) + "/content"
	if content != "" {
		p += "?content=" + url.QueryEscape(content)
	}
	if err := g.c.Get(ctx, p, &rows); err != nil {
		return nil, mapErr("query content of storage "+storage, err)
	}

	items := make([]types.StorageContentItem, 0, len(rows))
	for _, r := range rows {
		items = append(items, types.StorageContentItem{
			VolID:     r.VolID,
			Format:    r.Format,
			SizeBytes: int64(r.Size),
			Notes:     r.Notes,
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].VolID < items[j].VolID })
	return items, nil
}

// splitList splits a PVE separator-joined list ("iso,vztmpl,backup") into a
// clean slice, never nil.
func splitList(s, sep string) []string {
	out := []string{}
	for _, part := range strings.Split(s, sep) {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// flexInt64 decodes PVE byte counters that can arrive as plain integers,
// floats in scientific notation (observed above ~1PB), or quoted strings.
type flexInt64 int64

func (f *flexInt64) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	if i, err := strconv.ParseInt(s, 10, 64); err == nil {
		*f = flexInt64(i)
		return nil
	}
	fl, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return fmt.Errorf("flexInt64: cannot parse %q as a byte count", string(b))
	}
	*f = flexInt64(fl)
	return nil
}
