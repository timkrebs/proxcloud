package handlers

import (
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
// first renders the service's cloud-init server-side with the resolved superuser
// credential (user-supplied where the request carried one and it passed
// server-authoritative validation, generated otherwise) and hands the snippet to
// the deploy engine via CreateContext. A GENERATED password is surfaced ONCE in
// the response; a USER-SUPPLIED one is never echoed back. Either way the value is
// never stored, logged, or audited — only the user_credentials boolean is recorded.
func (d *Deps) ProvisionService(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIdentity(w, r)
	if !ok || !d.requireStore(w) {
		return
	}
	if !d.catalogReady(w) {
		return
	}
	// Degrade, don't crash: the catalog may be enabled while its SSH/SFTP snippet
	// writer failed to initialize at boot (missing SSH vars, an unreadable key, a bad
	// known_hosts). List/Get still work off the embedded defs, but provisioning
	// cannot place a snippet — fail honestly with 503 BEFORE any reserve/quota/deploy
	// work, so a catalog misconfig never leaks a reservation or hits Proxmox.
	if !d.CatalogProvisionReady {
		httpserver.WriteError(w, serviceUnavailable("catalog provisioning is unavailable: snippet writer is not configured"))
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
	// boolean — whether ANY credential was user-supplied — NEVER the credential
	// value (Phase A generated → "false"; Phase C user-supplied → "true").
	authz.Annotate(r.Context(), "vmid", strconv.Itoa(dep.VMID))
	authz.Annotate(r.Context(), "name", build.req.Name)
	authz.Annotate(r.Context(), "service", svc.ID)
	authz.Annotate(r.Context(), "user_credentials", strconv.FormatBool(build.anyUserSupplied))

	// The one-time reveal: return the generated password ONLY for a generated
	// credential. A user-supplied credential is never echoed back (the user already
	// has it); the non-secret CredentialHint records which case this was.
	reveal := build.password
	if build.userSupplied {
		reveal = ""
	}
	httpserver.WriteJSON(w, http.StatusAccepted, types.ProvisionServiceResponse{
		DeploymentID:      dep.ID,
		VMID:              dep.VMID,
		Username:          build.username,
		GeneratedPassword: reveal,
		CredentialHint:    build.credentialHint,
	})
}

// catalogProvision bundles the assembled create request, the rendered snippet,
// and the resolved credential outcome for ProvisionService.
type catalogProvision struct {
	req             types.CreateGuestRequest
	snippetContent  string
	snippetFilename string
	readinessPort   int
	username        string
	password        string // the primary credential value; one-time, never stored/logged/audited
	userSupplied    bool   // the primary credential's password was user-supplied (do NOT reveal it)
	anyUserSupplied bool   // ANY declared credential was user-supplied → audit "user_credentials"="true"
	credentialHint  string // NON-secret: "generated — shown once" vs "you set this credential"
}

// buildCatalogProvision assembles the qemu CreateGuestRequest from the service
// definition + request overrides, resolves each declared credential (user-supplied
// where given and valid, generated otherwise — server-authoritative validation
// BEFORE any reservation), and renders the cloud-init snippet. Sizing defaults to
// the service's default and must not dip below its minimum floor.
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

	// Resolve every declared credential: user-supplied where the request carried a
	// value (validated server-authoritatively — length-only password policy, charset
	// for a settable username), generated otherwise. A *catalog.CredentialError is a
	// 400 (weak/empty password, a username on a fixed-username credential); anything
	// else is a crypto/rand failure → 500. Both return here, BEFORE any reservation.
	// The raw values are used ONLY to build the base64 transport below and are never
	// logged, stored, audited, or echoed back.
	supplied := make([]catalog.SuppliedCredential, 0, len(req.Credentials))
	for _, c := range req.Credentials {
		supplied = append(supplied, catalog.SuppliedCredential{Name: c.Name, Username: c.Username, Password: c.Password})
	}
	resolved, err := catalog.ResolveCredentials(svc.Credentials, supplied, catalog.GeneratePassword)
	if err != nil {
		var ce *catalog.CredentialError
		if errors.As(err, &ce) {
			return nil, badRequest(ce.Msg)
		}
		d.logger().Error("resolve catalog credentials", "service", svc.ID, "err", err)
		return nil, &types.APIError{Code: "internal", Message: "Failed to prepare a credential.", Status: http.StatusInternalServerError}
	}
	// The template injects at most ONE credential slot (the service superuser). Most
	// services declare exactly one credential, so the primary is index 0; a service
	// with an EMPTY credential schema (e.g. Vault, ADR-0027 §4) injects none — it
	// self-initializes and Proxcloud never holds its secrets. anyUserSupplied drives
	// the audit boolean across all declared credentials.
	var username, password string
	var primaryUserSupplied bool
	anyUserSupplied := false
	if len(resolved) > 0 {
		primary := resolved[0]
		username, password, primaryUserSupplied = primary.Username, primary.Password, primary.UserSupplied
		for _, rc := range resolved {
			if rc.UserSupplied {
				anyUserSupplied = true
			}
		}
	}
	// Connection/reveal hints (never a secret). An empty-credential service points
	// the operator at its self-initialization steps instead of naming an account.
	credentialHintStr := "This service manages its own credentials — see the next steps to initialize it."
	revealHintStr := "no stored credential — see next steps"
	if len(resolved) > 0 {
		credentialHintStr = connectionHint(username, primaryUserSupplied)
		revealHintStr = revealHint(primaryUserSupplied)
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
			CredentialHint: credentialHintStr,
			UserSupplied:   anyUserSupplied,
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
		userSupplied:    primaryUserSupplied,
		anyUserSupplied: anyUserSupplied,
		credentialHint:  revealHintStr,
	}, nil
}

// revealHint is the NON-secret origin indicator returned in the provision
// response: whether the surfaced credential was generated (shown once) or set by
// the user (never echoed back). It never contains a credential value.
func revealHint(userSupplied bool) string {
	if userSupplied {
		return "you set this credential"
	}
	return "generated — shown once"
}

// connectionHint is the NON-secret auth hint carried on the deployment's
// connection view (CatalogProvision/Deployment.CredentialHint). It names the
// account/role only — never a password.
func connectionHint(username string, userSupplied bool) string {
	if userSupplied {
		return fmt.Sprintf("user %q — password you set at creation", username)
	}
	return fmt.Sprintf("user %q — password shown once at creation", username)
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
	roles := make([]types.CatalogRole, 0, len(s.Roles))
	for _, r := range s.Roles {
		roles = append(roles, types.CatalogRole{
			Name:  r.Name,
			Count: r.Count,
			Min:   r.Min,
			Max:   r.Max,
			Sizing: types.CatalogSizing{
				Default: types.CatalogSize{Cores: r.Sizing.Default.Cores, MemoryMB: r.Sizing.Default.MemoryMb, DiskGB: r.Sizing.Default.DiskGb},
				Min:     types.CatalogSize{Cores: r.Sizing.Min.Cores, MemoryMB: r.Sizing.Min.MemoryMb, DiskGB: r.Sizing.Min.DiskGb},
			},
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
		Roles:       roles,
		Credentials: creds,
		Ports:       s.Ports,
		Readiness:   s.Readiness,
		Docs:        s.Docs,
		TestedOn:    s.TestedOn,
	}
}
