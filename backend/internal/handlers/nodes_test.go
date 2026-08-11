package handlers_test

import (
	"context"
	"net/http"
	"reflect"
	"sync"
	"testing"
	"time"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	pmx "github.com/timkrebs9/proxcloud/backend/internal/proxmox"
	"github.com/timkrebs9/proxcloud/backend/internal/proxmox/proxmoxtest"
)

func nodeStatusFixture() *pmx.NodeStatusInfo {
	return &pmx.NodeStatusInfo{
		Uptime:        86400,
		CPUPct:        12.5,
		LoadAvg:       []float64{0.5, 0.4, 0.3},
		KernelVersion: "Linux 6.8.12-1-pve",
		CPUModel:      "AMD Ryzen 7",
		CPUCores:      8,
		CPUSockets:    1,
		MemUsed:       16 * gib,
		MemTotal:      32 * gib,
		SwapUsed:      1 * gib,
		SwapTotal:     8 * gib,
		RootFSUsed:    20 * gib,
		RootFSTotal:   100 * gib,
		PVEVersion:    "pve-manager/8.2.4",
	}
}

func TestListNodes(t *testing.T) {
	t.Run("fan-in: healthy, status-error, and cluster-offline nodes", func(t *testing.T) {
		var mu sync.Mutex
		statusCalls := []string{}
		mock := &proxmoxtest.MockClient{
			OnClusterResources: func(context.Context) ([]pmx.RawResource, error) {
				return []pmx.RawResource{
					// Out of name order on purpose: response must sort.
					{ID: "node/pve03", Type: "node", Node: "pve03", Status: "offline"},
					{ID: "node/pve01", Type: "node", Node: "pve01", Status: "online"},
					{ID: "node/pve02", Type: "node", Node: "pve02", Status: "online"},
					{ID: "qemu/101", Type: "qemu", Node: "pve01", VMID: 101, Status: "running"},
				}, nil
			},
			OnNodeStatus: func(ctx context.Context, node string) (*pmx.NodeStatusInfo, error) {
				mu.Lock()
				statusCalls = append(statusCalls, node)
				mu.Unlock()
				if _, ok := ctx.Deadline(); !ok {
					t.Errorf("NodeStatus(%s): context has no deadline", node)
				}
				switch node {
				case "pve01":
					return nodeStatusFixture(), nil
				case "pve02":
					return nil, pveUnreachable()
				default:
					t.Errorf("NodeStatus called for %s — cluster-offline nodes must not be probed", node)
					return nil, pveUnreachable()
				}
			},
		}

		rec := doGet(t, mock, "/api/nodes")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
		}
		got := decodeBody[[]types.NodeSummary](t, rec)
		if len(got) != 3 {
			t.Fatalf("len = %d, want 3 node rows: %+v", len(got), got)
		}

		st := nodeStatusFixture()
		want := []types.NodeSummary{
			{Node: "pve01", Online: true, CPUPct: st.CPUPct, MemUsed: st.MemUsed, MemTotal: st.MemTotal,
				DiskUsed: st.RootFSUsed, DiskTotal: st.RootFSTotal, UptimeSec: st.Uptime,
				PVEVersion: st.PVEVersion, LoadAvg: st.LoadAvg},
			{Node: "pve02", Online: false, LoadAvg: []float64{}},
			{Node: "pve03", Online: false, LoadAvg: []float64{}},
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("rows =\n%+v\nwant\n%+v", got, want)
		}

		mu.Lock()
		defer mu.Unlock()
		for _, n := range statusCalls {
			if n == "pve03" {
				t.Errorf("NodeStatus probed offline node pve03")
			}
		}
	})

	t.Run("proxmox unreachable surfaces as 502 envelope", func(t *testing.T) {
		mock := &proxmoxtest.MockClient{
			OnClusterResources: func(context.Context) ([]pmx.RawResource, error) { return nil, pveUnreachable() },
		}
		wantErrorEnvelope(t, doGet(t, mock, "/api/nodes"), http.StatusBadGateway, "proxmox_unreachable")
	})
}

func TestGetNode(t *testing.T) {
	tests := []struct {
		name       string
		mock       *proxmoxtest.MockClient
		target     string
		wantStatus int
		wantCode   string
		wantBody   *types.NodeDetail
	}{
		{
			name: "returns full detail",
			mock: &proxmoxtest.MockClient{
				OnNodeStatus: func(_ context.Context, node string) (*pmx.NodeStatusInfo, error) {
					if node != "pve01" {
						t.Errorf("node = %q, want pve01", node)
					}
					return nodeStatusFixture(), nil
				},
			},
			target:     "/api/nodes/pve01",
			wantStatus: http.StatusOK,
			wantBody: &types.NodeDetail{
				Node: "pve01", Online: true, CPUPct: 12.5,
				MemUsed: 16 * gib, MemTotal: 32 * gib,
				DiskUsed: 20 * gib, DiskTotal: 100 * gib,
				UptimeSec: 86400, PVEVersion: "pve-manager/8.2.4",
				LoadAvg:       []float64{0.5, 0.4, 0.3},
				KernelVersion: "Linux 6.8.12-1-pve", CPUModel: "AMD Ryzen 7",
				CPUCores: 8, CPUSockets: 1,
				SwapUsed: 1 * gib, SwapTotal: 8 * gib,
			},
		},
		{
			name: "unknown node surfaces as 404",
			mock: &proxmoxtest.MockClient{
				OnNodeStatus: func(context.Context, string) (*pmx.NodeStatusInfo, error) {
					return nil, &types.APIError{
						Code:       "not_found",
						Message:    "query status of node ghost: not found on Proxmox",
						PVEMessage: "595 no route to host",
						Status:     http.StatusNotFound,
					}
				},
			},
			target:     "/api/nodes/ghost",
			wantStatus: http.StatusNotFound,
			wantCode:   "not_found",
		},
		{
			name: "proxmox unreachable surfaces as 502",
			mock: &proxmoxtest.MockClient{
				OnNodeStatus: func(context.Context, string) (*pmx.NodeStatusInfo, error) {
					return nil, pveUnreachable()
				},
			},
			target:     "/api/nodes/pve01",
			wantStatus: http.StatusBadGateway,
			wantCode:   "proxmox_unreachable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := doGet(t, tt.mock, tt.target)
			if tt.wantCode != "" {
				wantErrorEnvelope(t, rec, tt.wantStatus, tt.wantCode)
				return
			}
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, tt.wantStatus, rec.Body.String())
			}
			got := decodeBody[types.NodeDetail](t, rec)
			if !reflect.DeepEqual(got, *tt.wantBody) {
				t.Errorf("detail =\n%+v\nwant\n%+v", got, *tt.wantBody)
			}
		})
	}
}

func TestGetNodeMetrics(t *testing.T) {
	sample := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	series := map[string][]types.MetricPoint{
		"cpu":     {{T: sample, V: 42.5}},
		"memused": {{T: sample, V: float64(16 * gib)}},
	}

	tests := []struct {
		name          string
		target        string
		wantTimeframe string
		wantStatus    int
		wantCode      string
	}{
		{name: "defaults to hour", target: "/api/nodes/pve01/metrics", wantTimeframe: "hour", wantStatus: http.StatusOK},
		{name: "passes timeframe through", target: "/api/nodes/pve01/metrics?timeframe=week", wantTimeframe: "week", wantStatus: http.StatusOK},
		{name: "invalid timeframe is a 400", target: "/api/nodes/pve01/metrics?timeframe=decade", wantTimeframe: "decade",
			wantStatus: http.StatusBadRequest, wantCode: "invalid_request"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &proxmoxtest.MockClient{
				OnNodeRRD: func(_ context.Context, node, timeframe string) (map[string][]types.MetricPoint, error) {
					if node != "pve01" {
						t.Errorf("node = %q, want pve01", node)
					}
					if timeframe != tt.wantTimeframe {
						t.Errorf("timeframe = %q, want %q", timeframe, tt.wantTimeframe)
					}
					// Mirror the real client's validation.
					if timeframe == "decade" {
						return nil, &types.APIError{Code: "invalid_request", Message: "invalid timeframe", Status: http.StatusBadRequest}
					}
					return series, nil
				},
			}
			rec := doGet(t, mock, tt.target)
			if tt.wantCode != "" {
				wantErrorEnvelope(t, rec, tt.wantStatus, tt.wantCode)
				return
			}
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, tt.wantStatus, rec.Body.String())
			}
			got := decodeBody[types.MetricsResponse](t, rec)
			if got.Timeframe != tt.wantTimeframe {
				t.Errorf("timeframe = %q, want %q", got.Timeframe, tt.wantTimeframe)
			}
			if len(got.Series["cpu"]) != 1 || got.Series["cpu"][0].V != 42.5 || !got.Series["cpu"][0].T.Equal(sample) {
				t.Errorf("cpu series = %+v", got.Series["cpu"])
			}
		})
	}
}

func TestNodeSubresources(t *testing.T) {
	t.Run("bridges", func(t *testing.T) {
		mock := &proxmoxtest.MockClient{
			OnNodeBridges: func(_ context.Context, node string) ([]types.Bridge, error) {
				if node != "pve01" {
					t.Errorf("node = %q, want pve01", node)
				}
				return []types.Bridge{{Iface: "vmbr0", Active: true, Comment: "lan"}}, nil
			},
		}
		rec := doGet(t, mock, "/api/nodes/pve01/bridges")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d (body %q)", rec.Code, rec.Body.String())
		}
		got := decodeBody[[]types.Bridge](t, rec)
		if len(got) != 1 || got[0].Iface != "vmbr0" || !got[0].Active {
			t.Errorf("bridges = %+v", got)
		}
	})

	t.Run("storages passes content filter through", func(t *testing.T) {
		mock := &proxmoxtest.MockClient{
			OnNodeStorages: func(_ context.Context, node, content string) ([]types.NodeStorage, error) {
				if node != "pve01" || content != "iso" {
					t.Errorf("args = (%q, %q), want (pve01, iso)", node, content)
				}
				return []types.NodeStorage{{Storage: "local", Type: "dir", Content: []string{"iso"}, Active: true, Enabled: true}}, nil
			},
		}
		rec := doGet(t, mock, "/api/nodes/pve01/storages?content=iso")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d (body %q)", rec.Code, rec.Body.String())
		}
		got := decodeBody[[]types.NodeStorage](t, rec)
		if len(got) != 1 || got[0].Storage != "local" {
			t.Errorf("storages = %+v", got)
		}
	})

	t.Run("storage content passes node, storage, and filter through", func(t *testing.T) {
		mock := &proxmoxtest.MockClient{
			OnStorageContent: func(_ context.Context, node, storage, content string) ([]types.StorageContentItem, error) {
				if node != "pve01" || storage != "local" || content != "vztmpl" {
					t.Errorf("args = (%q, %q, %q), want (pve01, local, vztmpl)", node, storage, content)
				}
				return []types.StorageContentItem{{VolID: "local:vztmpl/debian-12.tar.zst", Format: "tzst", SizeBytes: 123}}, nil
			},
		}
		rec := doGet(t, mock, "/api/nodes/pve01/storages/local/content?content=vztmpl")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d (body %q)", rec.Code, rec.Body.String())
		}
		got := decodeBody[[]types.StorageContentItem](t, rec)
		if len(got) != 1 || got[0].VolID != "local:vztmpl/debian-12.tar.zst" {
			t.Errorf("content = %+v", got)
		}
	})
}
