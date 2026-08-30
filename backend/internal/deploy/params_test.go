package deploy

import (
	"net/url"
	"strings"
	"testing"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
)

func lxcReq() *types.CreateGuestRequest {
	return &types.CreateGuestRequest{
		Type: "lxc", Name: "cache-01", Node: "pve01", VMID: 200,
		Source: types.CreateSource{Mode: "vztmpl", VztmplVolID: "local-data:vztmpl/debian-12-standard_12.7-1_amd64.tar.zst"},
		Cores:  2, MemoryMB: 2048, DiskGB: 8, Storage: "local-lvm", Bridge: "vmbr0",
	}
}

func vmReq() *types.CreateGuestRequest {
	return &types.CreateGuestRequest{
		Type: "qemu", Name: "web-02", Node: "pve01", VMID: 106,
		Source: types.CreateSource{Mode: "iso", ISOVolID: "local-data:iso/debian-12.iso"},
		Cores:  4, MemoryMB: 8192, DiskGB: 32, Storage: "local-lvm", Bridge: "vmbr0",
	}
}

func TestBuildCreateParamsLXC(t *testing.T) {
	t.Run("basic dhcp", func(t *testing.T) {
		p, err := BuildCreateParams(lxcReq())
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]any{
			"vmid": 200, "hostname": "cache-01", "cores": 2, "memory": int64(2048),
			"ostemplate": "local-data:vztmpl/debian-12-standard_12.7-1_amd64.tar.zst",
			"rootfs":     "local-lvm:8", "unprivileged": 1,
			"net0": "name=eth0,bridge=vmbr0,ip=dhcp",
		}
		for k, v := range want {
			if p[k] != v {
				t.Errorf("%s = %#v, want %#v", k, p[k], v)
			}
		}
		if _, ok := p["pool"]; ok {
			t.Error("pool set without request")
		}
	})

	t.Run("static ip + vlan + firewall + pool + tags + keys", func(t *testing.T) {
		req := lxcReq()
		req.Pool = "homelab"
		req.VLANTag = 20
		req.Firewall = true
		req.Tags = []string{"env-prod", "web"}
		req.IPConfig = &types.IPConfig{Mode: "static", CIDR: "192.168.1.50/24", Gateway: "192.168.1.1"}
		req.CloudInit = &types.CloudInitRequest{Password: "s3cret", SSHKeys: []string{"ssh-ed25519 AAAA key1"}}

		p, err := BuildCreateParams(req)
		if err != nil {
			t.Fatal(err)
		}
		if p["net0"] != "name=eth0,bridge=vmbr0,tag=20,firewall=1,ip=192.168.1.50/24,gw=192.168.1.1" {
			t.Errorf("net0 = %v", p["net0"])
		}
		if p["pool"] != "homelab" || p["tags"] != "env-prod;web" {
			t.Errorf("pool/tags = %v/%v", p["pool"], p["tags"])
		}
		if p["password"] != "s3cret" || p["ssh-public-keys"] != "ssh-ed25519 AAAA key1" {
			t.Errorf("provisioning params = %v / %v", p["password"], p["ssh-public-keys"])
		}
	})
}

func TestBuildCreateParamsVM(t *testing.T) {
	t.Run("iso basic", func(t *testing.T) {
		p, err := BuildCreateParams(vmReq())
		if err != nil {
			t.Fatal(err)
		}
		checks := map[string]any{
			"vmid": 106, "name": "web-02", "cores": 4, "memory": int64(8192),
			"scsi0": "local-lvm:32", "ide2": "local-data:iso/debian-12.iso,media=cdrom",
			"boot": "order=scsi0;ide2", "agent": 1, "scsihw": "virtio-scsi-single",
			"net0": "virtio,bridge=vmbr0",
		}
		for k, v := range checks {
			if p[k] != v {
				t.Errorf("%s = %#v, want %#v", k, p[k], v)
			}
		}
		if _, ok := p["ide0"]; ok {
			t.Error("cloud-init drive present without cloud-init request")
		}
	})

	t.Run("cloud-init full", func(t *testing.T) {
		req := vmReq()
		keys := []string{"ssh-ed25519 AAAA key1", "ssh-rsa BBBB key2"}
		req.CloudInit = &types.CloudInitRequest{User: "admin", Password: "pw", SSHKeys: keys, Nameserver: "1.1.1.1"}
		req.IPConfig = &types.IPConfig{Mode: "static", CIDR: "10.0.0.5/24", Gateway: "10.0.0.1"}

		p, err := BuildCreateParams(req)
		if err != nil {
			t.Fatal(err)
		}
		if p["ide0"] != "local-lvm:cloudinit" || p["ciuser"] != "admin" || p["cipassword"] != "pw" {
			t.Errorf("cloud-init params: ide0=%v ciuser=%v", p["ide0"], p["ciuser"])
		}
		if p["ipconfig0"] != "ip=10.0.0.5/24,gw=10.0.0.1" {
			t.Errorf("ipconfig0 = %v", p["ipconfig0"])
		}
		// sshkeys must be URL-encoded, decoding back to the joined keys.
		enc, _ := p["sshkeys"].(string)
		dec, err := url.QueryUnescape(enc)
		if err != nil || dec != strings.Join(keys, "\n") {
			t.Errorf("sshkeys encoding round-trip failed: %q -> %q (%v)", enc, dec, err)
		}
	})
}

func TestBuildCloneParams(t *testing.T) {
	base := func(mode string) *types.CreateGuestRequest {
		return &types.CreateGuestRequest{
			Type: "qemu", Name: "web-03", Node: "pve01", VMID: 107,
			Source: types.CreateSource{Mode: "clone", CloneVMID: 9000, CloneMode: mode},
			Cores:  4, MemoryMB: 8192, Storage: "local-lvm",
		}
	}

	full, err := BuildCloneParams(base("full"))
	if err != nil {
		t.Fatal(err)
	}
	if !full.Full || full.Storage != "local-lvm" || full.NewVMID != 107 || full.Name != "web-03" {
		t.Errorf("full clone = %+v", full)
	}

	linked, err := BuildCloneParams(base("linked"))
	if err != nil {
		t.Fatal(err)
	}
	// Linked clones must NOT carry a storage parameter — PVE rejects it.
	if linked.Full || linked.Storage != "" {
		t.Errorf("linked clone = %+v, storage must be empty", linked)
	}
}

func TestValidateRejects(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*types.CreateGuestRequest)
		want   string
	}{
		{"bad name", func(r *types.CreateGuestRequest) { r.Name = "Web_02" }, "name must start"},
		{"low vmid", func(r *types.CreateGuestRequest) { r.VMID = 99 }, "vmid"},
		{"zero cores", func(r *types.CreateGuestRequest) { r.Cores = 0 }, "cores"},
		{"tiny memory", func(r *types.CreateGuestRequest) { r.MemoryMB = 64 }, "memory"},
		{"iso on lxc", func(r *types.CreateGuestRequest) {
			r.Type = "lxc"
			r.Source = types.CreateSource{Mode: "iso", ISOVolID: "x"}
		}, "only valid for qemu"},
		{"missing template", func(r *types.CreateGuestRequest) { r.Type = "lxc"; r.Source = types.CreateSource{Mode: "vztmpl"} }, "template volume"},
		{"bad cidr", func(r *types.CreateGuestRequest) { r.IPConfig = &types.IPConfig{Mode: "static", CIDR: "not-a-cidr"} }, "CIDR"},
		{"bad tag", func(r *types.CreateGuestRequest) { r.Tags = []string{"Bad Tag"} }, "tag"},
		{"bad vlan", func(r *types.CreateGuestRequest) { r.VLANTag = 5000 }, "vlan"},
		{"clone without mode", func(r *types.CreateGuestRequest) { r.Source = types.CreateSource{Mode: "clone", CloneVMID: 9000} }, "clone mode"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := vmReq()
			tt.mutate(req)
			err := Validate(req)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want contains %q", err, tt.want)
			}
		})
	}
}
