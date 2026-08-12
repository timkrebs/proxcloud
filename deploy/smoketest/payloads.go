package main

// Minimal mirrors of the public Proxcloud API JSON shapes the smoke test
// touches. These are deliberately a SUBSET of backend/api/types — the smoke
// binary must not import backend internals (ADR-0016: black-box API contract),
// so the few fields it asserts on are re-declared here as a stable contract.

// versionInfo mirrors GET /api/v1/version. The wave asserts .commit for a
// 40-hex SHA deploy and .semver for a vX.Y.Z tag deploy.
type versionInfo struct {
	Commit    string `json:"commit"`
	Semver    string `json:"semver"`
	BuildTime string `json:"buildTime"`
}

// loginRequest is POST /api/auth/login. Session login (per the WS5 decision):
// email+password, not a PAT.
type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// loginResponse is the 200 body of POST /api/auth/login. A seeded smoke user
// must NOT have TOTP enabled (ADR-0016 §3), so totpRequired must be false and a
// proxcloud_session cookie must be set.
type loginResponse struct {
	TotpRequired bool `json:"totpRequired"`
}

// meResponse is the subset of GET /api/auth/me used to resolve the smoke
// tenant id from a slug and to confirm membership.
type meResponse struct {
	Email   string             `json:"email"`
	Tenants []tenantMembership `json:"tenants"`
}

type tenantMembership struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
	Role string `json:"role"`
}

// project mirrors one entry of GET /api/tenants/{tenantId}/projects, used to
// resolve the smoke project id from a slug.
type project struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

// guestSummary is the subset of one GET /api/tenants/{tenantId}/resources row
// the smoke test needs to detect and clean up a stale reserved VMID.
type guestSummary struct {
	Type   string `json:"type"`
	VMID   int    `json:"vmid"`
	Name   string `json:"name"`
	Node   string `json:"node"`
	Status string `json:"status"`
}

// createSource selects the guest source; for the throwaway LXC the smoke test
// uses a vztmpl (container template) volume id.
type createSource struct {
	Mode        string `json:"mode"`
	VztmplVolID string `json:"vztmplVolId,omitempty"`
}

// createGuestRequest is the subset of the wizard submit payload needed to
// create a minimal LXC (ADR-0016 §1.4). startAfterCreate is false so the guest
// is stopped and therefore deletable (DeleteGuest rejects a running guest).
type createGuestRequest struct {
	Type             string       `json:"type"`
	Name             string       `json:"name"`
	Node             string       `json:"node"`
	VMID             int          `json:"vmid"`
	ProjectID        string       `json:"projectId"`
	Source           createSource `json:"source"`
	Cores            int          `json:"cores"`
	MemoryMB         int64        `json:"memoryMb"`
	DiskGB           int          `json:"diskGb"`
	Storage          string       `json:"storage"`
	Bridge           string       `json:"bridge"`
	StartAfterCreate bool         `json:"startAfterCreate"`
}

// createGuestResponse is the 202 acknowledgement of an accepted deployment.
type createGuestResponse struct {
	DeploymentID string `json:"deploymentId"`
	VMID         int    `json:"vmid"`
}

// deployment is the subset of GET /api/tenants/{tenantId}/deployments/{id}.
// Status is running | succeeded | failed (honest task states, never faked).
type deployment struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	VMID   int    `json:"vmid"`
}

// deleteRequest is the DELETE guest body; confirmName must equal the guest's
// real name or the backend rejects the deletion.
type deleteRequest struct {
	ConfirmName string `json:"confirmName"`
}

// taskRef is the 202 body of every async mutation (here: the delete task).
type taskRef struct {
	UPID   string `json:"upid"`
	Action string `json:"action"`
}

// taskSummary is the subset of GET /api/tenants/{tenantId}/tasks/{upid} used to
// poll the delete task to completion. Status is running | succeeded | failed.
type taskSummary struct {
	UPID       string `json:"upid"`
	Status     string `json:"status"`
	ExitStatus string `json:"exitStatus,omitempty"`
}

// apiError mirrors the backend error envelope for readable failure detail.
type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"status"`
}
