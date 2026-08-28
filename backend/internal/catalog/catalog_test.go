package catalog

import (
	"strings"
	"testing"
	"testing/fstest"
)

// TestLoadEmbedded loads the real embedded catalog and asserts the PostgreSQL
// reference service parsed with the expected shape. This is the fail-fast
// startup path — a broken embedded def would fail here (and at boot).
func TestLoadEmbedded(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(c.List()) == 0 {
		t.Fatal("catalog loaded no services")
	}
	pg, ok := c.Get("postgresql")
	if !ok {
		t.Fatal("postgresql service not found")
	}
	if pg.Kind != "single" || pg.GuestType != "qemu" {
		t.Errorf("kind/guestType = %q/%q, want single/qemu", pg.Kind, pg.GuestType)
	}
	if pg.Sizing.Default.Cores < pg.Sizing.Min.Cores {
		t.Errorf("default cores %d below min %d", pg.Sizing.Default.Cores, pg.Sizing.Min.Cores)
	}
	if got := pg.PrimaryPort(); got != 5432 {
		t.Errorf("primary port = %d, want 5432", got)
	}
	port, ok := pg.ReadinessPort()
	if !ok || port != 5432 {
		t.Errorf("readiness port = %d,%v, want 5432,true", port, ok)
	}
	if len(pg.Credentials) == 0 || pg.Credentials[0].Name != "superuser" {
		t.Errorf("credentials = %+v, want one 'superuser'", pg.Credentials)
	}
	if pg.cloudInit == nil || pg.nextSteps == nil {
		t.Error("templates not parsed at load")
	}
	// Get on an unknown id is a clean miss (the handler maps it to 404).
	if _, ok := c.Get("nope"); ok {
		t.Error("Get returned an unknown service")
	}
}

// validFiles returns a minimal valid service definition tree for one id, usable
// as a fstest.MapFS the loader accepts, so tests can mutate one field to prove a
// specific validation rejection.
func validFiles(id string) map[string]string {
	svc := `id: ` + id + `
displayName: Test
description: A test service
icon: database
category: database
kind: single
guestType: qemu
baseImage:
  ref: "local:iso/img.img"
sizing:
  default: { cores: 2, memoryMb: 2048, diskGb: 16 }
  min: { cores: 1, memoryMb: 1024, diskGb: 8 }
credentials:
  - name: superuser
    username: postgres
    userSettable: true
    generatedDefault: true
ports: [5432]
readiness: "tcp:5432"
docs: "https://example.com"
testedOn: "2026-08-28"
`
	return map[string]string{
		"services/" + id + "/service.yaml":          svc,
		"services/" + id + "/cloud-init.yaml.tftpl": "#cloud-config\nhostname: {{ .Hostname }}\n",
		"services/" + id + "/next-steps.md.tftpl":   "Host {{ .Host }}:{{ .Port }}\n",
	}
}

func mapFS(files map[string]string) fstest.MapFS {
	m := fstest.MapFS{}
	for k, v := range files {
		m[k] = &fstest.MapFile{Data: []byte(v)}
	}
	return m
}

func TestLoadValidatesFailFast(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(map[string]string)
		wantSub string
	}{
		{"happy path", func(map[string]string) {}, ""},
		{"id mismatch dir", func(f map[string]string) {
			f["services/pg/service.yaml"] = strings.Replace(f["services/pg/service.yaml"], "id: pg", "id: other", 1)
		}, "must equal its directory name"},
		{"bad kind set", func(f map[string]string) {
			f["services/pg/service.yaml"] = strings.Replace(f["services/pg/service.yaml"], "kind: single", "kind: set", 1)
		}, "reserved"},
		{"lxc rejected", func(f map[string]string) {
			f["services/pg/service.yaml"] = strings.Replace(f["services/pg/service.yaml"], "guestType: qemu", "guestType: lxc", 1)
		}, "guestType must be 'qemu'"},
		{"default below min", func(f map[string]string) {
			f["services/pg/service.yaml"] = strings.Replace(f["services/pg/service.yaml"], "default: { cores: 2, memoryMb: 2048, diskGb: 16 }", "default: { cores: 1, memoryMb: 512, diskGb: 4 }", 1)
		}, "must be >="},
		{"bad readiness", func(f map[string]string) {
			f["services/pg/service.yaml"] = strings.Replace(f["services/pg/service.yaml"], `readiness: "tcp:5432"`, `readiness: "http:8080"`, 1)
		}, "readiness must be 'tcp:<port>'"},
		{"no credentials", func(f map[string]string) {
			f["services/pg/service.yaml"] = strings.Replace(f["services/pg/service.yaml"], "credentials:\n  - name: superuser\n    username: postgres\n    userSettable: true\n    generatedDefault: true", "credentials: []", 1)
		}, "at least one credential"},
		{"unknown field", func(f map[string]string) {
			f["services/pg/service.yaml"] += "bogusField: 1\n"
		}, "parse"},
		{"bad testedOn", func(f map[string]string) {
			f["services/pg/service.yaml"] = strings.Replace(f["services/pg/service.yaml"], `testedOn: "2026-08-28"`, `testedOn: "yesterday"`, 1)
		}, "testedOn must be YYYY-MM-DD"},
		{"missing template", func(f map[string]string) {
			delete(f, "services/pg/cloud-init.yaml.tftpl")
		}, "cloud-init.yaml.tftpl"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files := validFiles("pg")
			tt.mutate(files)
			_, err := loadFS(mapFS(files), "services")
			if tt.wantSub == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantSub) {
				t.Fatalf("err = %v, want contains %q", err, tt.wantSub)
			}
		})
	}
}

func TestLoadRejectsDuplicateID(t *testing.T) {
	files := validFiles("pg")
	// A second directory declaring the same id → duplicate.
	files["services/dup/service.yaml"] = strings.Replace(validFiles("dup")["services/dup/service.yaml"], "id: dup", "id: pg", 1)
	files["services/dup/cloud-init.yaml.tftpl"] = "#cloud-config\n"
	files["services/dup/next-steps.md.tftpl"] = "x\n"
	if _, err := loadFS(mapFS(files), "services"); err == nil || !strings.Contains(err.Error(), "id") {
		t.Fatalf("err = %v, want a duplicate-id error", err)
	}
}
