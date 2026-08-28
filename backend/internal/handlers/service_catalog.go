package handlers

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/authz"
	"github.com/timkrebs9/proxcloud/backend/internal/bootstrap"
	"github.com/timkrebs9/proxcloud/backend/internal/catalog"
	"github.com/timkrebs9/proxcloud/backend/internal/deploy"
	"github.com/timkrebs9/proxcloud/backend/internal/httpserver"
	"github.com/timkrebs9/proxcloud/backend/internal/store"
)

// catalogReady guards the service-catalog routes: with the feature off (the
// default) the catalog is not loaded, so the endpoints report "not enabled" as a
// 404 (no capability leak). The routes are always mounted so the permission/audit
// completeness tests stay green; behavior is gated here.
func (d *Deps) catalogReady(w http.ResponseWriter) bool {
	if !d.CatalogEnabled || d.Catalog == nil {
		httpserver.WriteError(w, notFound("The service catalog is not enabled."))
		return false
	}
	return true
}

// ListServices serves GET /api/tenants/{tenantId}/service-catalog (Reader): the
// gallery of platform services. The catalog is global, so every tenant sees the
// same set (ADR-0026).
func (d *Deps) ListServices(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireIdentity(w, r); !ok {
		return
	}
	if !d.catalogReady(w) {
		return
	}
	defs := d.Catalog.List()
	out := make([]types.CatalogService, 0, len(defs))
	for _, s := range defs {
		out = append(out, toCatalogService(s))
	}
	httpserver.WriteJSON(w, http.StatusOK, types.CatalogServiceList{Services: out})
}

// GetService serves GET /api/tenants/{tenantId}/service-catalog/{serviceId}
// (Reader). {serviceId} is a global catalog id, not a tenant resource — an
// unknown id is a genuine 404 (no tenant existence leak).
func (d *Deps) GetService(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireIdentity(w, r); !ok {
		return
	}
	if !d.catalogReady(w) {
		return
	}
	svc, ok := d.Catalog.Get(chi.URLParam(r, "serviceId"))
	if !ok {
		httpserver.WriteError(w, notFound("Service not found."))
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toCatalogService(svc))
}

// ProvisionService serves POST
// /api/tenants/{tenantId}/service-catalog/{serviceId}/provision (Contributor). It
// reuses the CreateGuest spine — resolve project→pool (cross-tenant 404), reserve
// the VMID+quota BEFORE any Proxmox call, ensure the pool, then submit — but
// first renders the service's cloud-init server-side with a generated superuser
// password and hands the snippet to the deploy engine via CreateContext. The
// generated password is surfaced ONCE in the response and never stored, logged,
// or audited (only the user_credentials boolean is recorded).
func (d *Deps) ProvisionService(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIdentity(w, r)
	if !ok || !d.requireStore(w) {
		return
	}
	if !d.catalogReady(w) {
		return
	}
	if d.Deploy == nil {
		httpserver.WriteError(w, &types.APIError{Code: "internal", Message: "deployment engine not configured", Status: http.StatusInternalServerError})
		return
	}
	svc, ok := d.Catalog.Get(chi.URLParam(r, "serviceId"))
	if !ok {
		httpserver.WriteError(w, notFound("Service not found."))
		return
	}

	var req types.ProvisionServiceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpserver.WriteError(w, badRequest("body must be a JSON ProvisionServiceRequest"))
		return
	}
	tenantID := id.ActiveTenantID
	if req.ProjectId == "" {
		httpserver.WriteError(w, badRequest("projectId is required"))
		return
	}

	// Resolve the project → pool. Cross-tenant project → 404 (no existence leak).
	proj, err := d.Store.GetProjectByID(r.Context(), req.ProjectId)
	if errors.Is(err, store.ErrNotFound) {
		httpserver.WriteError(w, notFound("Project not found."))
		return
	}
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	if proj.TenantID != tenantID {
		httpserver.WriteError(w, notFound("Project not found."))
		return
	}

	// Build the CreateGuestRequest + render the snippet (server-authoritative
	// credentials). buildCatalogProvision validates the assembled request (before
	// rendering) and rejects an empty SSH-key set, so a bad request returns here
	// BEFORE any reservation — never an orphan row.
	build, err := d.buildCatalogProvision(svc, &req)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}

	actor := id.UserID

	// Live allocation snapshot for the quota join (one PVE round-trip, outside the
	// store's advisory lock — mirrors CreateGuest, ADR-0009).
	snap, err := d.clusterSnapshot(r)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}

	reserved := store.Alloc{VCPU: build.req.Cores, RAMMB: build.req.MemoryMB, DiskGB: int64(build.req.DiskGB)}
	own, err := d.Store.ReserveOwnership(r.Context(), store.ReserveOwnershipParams{
		TenantID:  tenantID,
		ProjectID: proj.ID,
		VMID:      build.req.VMID,
		GuestType: build.req.Type,
		Node:      build.req.Node,
		CreatedBy: &actor,
		Reserved:  reserved,
		Snapshot:  snap,
	})
	if err != nil {
		var qe store.ErrQuotaExceeded
		switch {
		case errors.As(err, &qe):
			httpserver.WriteError(w, &types.APIError{Code: "quota_exceeded", Message: quotaExceededMessage(qe), Status: http.StatusConflict})
		case errors.Is(err, store.ErrConflict):
			httpserver.WriteError(w, &types.APIError{Code: "conflict", Message: "That VMID is already reserved or in use.", Status: http.StatusConflict})
		default:
			d.logger().Error("reserve ownership (catalog)", "vmid", build.req.VMID, "err", err)
			httpserver.WriteError(w, &types.APIError{Code: "internal", Message: "Failed to reserve the VMID.", Status: http.StatusInternalServerError})
		}
		return
	}

	// Ensure the pool exists AFTER the reservation clears. A pool failure frees the
	// reservation so it does not leak quota.
	if err := bootstrap.EnsureProjectPool(r.Context(), d.PVE, proj.PoolID, poolComment); err != nil {
		if relErr := d.Store.ReleaseOwnership(r.Context(), own.ID); relErr != nil {
			d.logger().Warn("release ownership after pool failure (catalog)", "err", relErr)
		}
		httpserver.WriteError(w, err)
		return
	}
	build.req.Pool = proj.PoolID

	cctx := deploy.CreateContext{
		TenantID:        tenantID,
		ProjectID:       proj.ID,
		PoolID:          proj.PoolID,
		ActorUserID:     actor,
		OwnershipID:     own.ID,
		SnippetContent:  build.snippetContent,
		SnippetFilename: build.snippetFilename,
		ReadinessPort:   build.readinessPort,
	}
	dep, err := d.Deploy.Submit(&build.req, cctx)
	if err != nil {
		if relErr := d.Store.ReleaseOwnership(r.Context(), own.ID); relErr != nil {
			d.logger().Warn("release ownership after submit failure (catalog)", "err", relErr)
		}
		httpserver.WriteError(w, err)
		return
	}

	// Audit enrichment: the resolved guest + service, and the user_credentials
	// boolean — NEVER the credential value (Phase A generates → "false").
	authz.Annotate(r.Context(), "vmid", strconv.Itoa(dep.VMID))
	authz.Annotate(r.Context(), "name", build.req.Name)
	authz.Annotate(r.Context(), "service", svc.ID)
	authz.Annotate(r.Context(), "user_credentials", "false")

	// The generated password is surfaced HERE, once, and nowhere else.
	httpserver.WriteJSON(w, http.StatusAccepted, types.ProvisionServiceResponse{
		DeploymentID:      dep.ID,
		VMID:              dep.VMID,
		Username:          build.username,
		GeneratedPassword: build.password,
	})
}

// catalogProvision bundles the assembled create request, the rendered snippet,
// and the one-time generated credential for ProvisionService.
type catalogProvision struct {
	req             types.CreateGuestRequest
	snippetContent  string
	snippetFilename string
	readinessPort   int
	username        string
	password        string // one-time; never stored/logged/audited
}

// buildCatalogProvision assembles the qemu CreateGuestRequest from the service
// definition + request overrides, generates the superuser password, and renders
// the cloud-init snippet. Sizing defaults to the service's default and must not
// dip below its minimum floor.
func (d *Deps) buildCatalogProvision(svc *catalog.ServiceDef, req *types.ProvisionServiceRequest) (*catalogProvision, error) {
	cores := req.Cores
	if cores == 0 {
		cores = svc.Sizing.Default.Cores
	}
	mem := req.MemoryMB
	if mem == 0 {
		mem = svc.Sizing.Default.MemoryMb
	}
	disk := req.DiskGB
	if disk == 0 {
		disk = svc.Sizing.Default.DiskGb
	}
	if cores < svc.Sizing.Min.Cores {
		return nil, badRequest(fmt.Sprintf("cores must be at least %d for %s", svc.Sizing.Min.Cores, svc.ID))
	}
	if mem < svc.Sizing.Min.MemoryMb {
		return nil, badRequest(fmt.Sprintf("memory must be at least %d MiB for %s", svc.Sizing.Min.MemoryMb, svc.ID))
	}
	if disk < svc.Sizing.Min.DiskGb {
		return nil, badRequest(fmt.Sprintf("disk must be at least %d GiB for %s", svc.Sizing.Min.DiskGb, svc.ID))
	}

	// A catalog guest locks password login (lock_passwd: true) and cicustom drops
	// cipassword, so an SSH key is the ONLY way in. Reject a provision with no key
	// rather than boot an unreachable guest (returns before any reservation).
	if !hasSSHKey(req.SSHKeys) {
		return nil, badRequest("at least one SSH public key is required — a catalog guest locks password login, so an SSH key is the only way to log in")
	}

	// The superuser username is fixed by the definition (the built-in role).
	username := svc.Credentials[0].Username
	if username == "" {
		username = svc.Credentials[0].Name
	}
	password, err := generatePassword()
	if err != nil {
		d.logger().Error("generate catalog password", "err", err)
		return nil, &types.APIError{Code: "internal", Message: "Failed to generate a credential.", Status: http.StatusInternalServerError}
	}

	filename := fmt.Sprintf("proxcloud-%d-%s.yaml", req.VMID, svc.ID)
	snippetRef := d.SnippetDatastore + ":snippets/" + filename
	port := svc.PrimaryPort()

	// Default to DHCP so the guest gets an address the configuring step can see.
	ipcfg := req.IPConfig
	if ipcfg == nil {
		ipcfg = &types.IPConfig{Mode: "dhcp"}
	}

	cg := types.CreateGuestRequest{
		Type:      "qemu",
		Name:      req.Name,
		Node:      req.Node,
		VMID:      req.VMID,
		ProjectId: req.ProjectId,
		// Import the cloud image as the boot DISK — a raw cloud .img is not a
		// bootable CD-ROM, so image mode is what actually boots + runs cloud-init.
		Source:   types.CreateSource{Mode: "image", ImageVolID: svc.BaseImage.Ref},
		Cores:    cores,
		MemoryMB: mem,
		DiskGB:   disk,
		Storage:  req.Storage,
		Bridge:   req.Bridge,
		VLANTag:  req.VLANTag,
		Firewall: req.Firewall,
		IPConfig: ipcfg,
		Tags:     req.Tags,
		// A catalog guest must start so the configuring step can reach it.
		StartAfterCreate: true,
		Catalog: &types.CatalogProvision{
			ServiceID:      svc.ID,
			SnippetRef:     snippetRef,
			Ports:          svc.Ports,
			CredentialHint: fmt.Sprintf("user %q — password shown once at creation", username),
			UserSupplied:   false,
		},
	}

	// Validate the assembled request BEFORE rendering the snippet, so an invalid
	// name (which becomes the guest hostname in the template) is rejected before it
	// is ever interpolated.
	if err := deploy.Validate(&cg); err != nil {
		return nil, badRequest(err.Error())
	}

	content, err := svc.RenderCloudInit(catalog.CloudInitInput{
		Hostname:         req.Name,
		LoginUser:        "proxcloud",
		SSHKeysB64:       catalog.B64Each(req.SSHKeys),
		SuperuserUserB64: catalog.B64(username),
		SuperuserPassB64: catalog.B64(password),
		ListenAddresses:  "*",
		Port:             port,
	})
	if err != nil {
		d.logger().Error("render catalog cloud-init", "service", svc.ID, "err", err)
		return nil, &types.APIError{Code: "internal", Message: "Failed to render the service configuration.", Status: http.StatusInternalServerError}
	}

	return &catalogProvision{
		req:             cg,
		snippetContent:  content,
		snippetFilename: filename,
		readinessPort:   port,
		username:        username,
		password:        password,
	}, nil
}

// hasSSHKey reports whether at least one non-blank SSH public key was supplied.
func hasSSHKey(keys []string) bool {
	for _, k := range keys {
		if strings.TrimSpace(k) != "" {
			return true
		}
	}
	return false
}

// generatePassword mints a strong, URL-safe secret (length-only policy ≥ 12;
// 18 crypto/rand bytes → 24 chars). It is never persisted, logged, or audited.
func generatePassword() (string, error) {
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// toCatalogService maps a loaded definition to its frontend wire view.
func toCatalogService(s *catalog.ServiceDef) types.CatalogService {
	creds := make([]types.CatalogCredential, 0, len(s.Credentials))
	for _, c := range s.Credentials {
		creds = append(creds, types.CatalogCredential{
			Name:             c.Name,
			Username:         c.Username,
			UsernameSettable: c.UsernameSettable,
			UserSettable:     c.UserSettable,
			GeneratedDefault: c.GeneratedDefault,
		})
	}
	return types.CatalogService{
		ID:          s.ID,
		DisplayName: s.DisplayName,
		Description: s.Description,
		Icon:        s.Icon,
		Category:    s.Category,
		Kind:        s.Kind,
		GuestType:   s.GuestType,
		Sizing: types.CatalogSizing{
			Default: types.CatalogSize{Cores: s.Sizing.Default.Cores, MemoryMB: s.Sizing.Default.MemoryMb, DiskGB: s.Sizing.Default.DiskGb},
			Min:     types.CatalogSize{Cores: s.Sizing.Min.Cores, MemoryMB: s.Sizing.Min.MemoryMb, DiskGB: s.Sizing.Min.DiskGb},
		},
		Credentials: creds,
		Ports:       s.Ports,
		Readiness:   s.Readiness,
		Docs:        s.Docs,
		TestedOn:    s.TestedOn,
	}
}
