package types

import "time"

// DeploymentSet is the frontend-facing view of a deployment set (ADR-0029): one
// catalog action that provisioned N linked guests sharing a lifecycle. Status is
// the durable set status (provisioning | ready | degraded | failed | deleting).
// It never carries a secret — the cluster's generated join token is never stored,
// returned, or surfaced (ADR-0030).
type DeploymentSet struct {
	ID        string                `json:"id"`
	ServiceID string                `json:"serviceId"`
	ProjectId string                `json:"projectId"`
	Status    string                `json:"status"`
	CreatedAt time.Time             `json:"createdAt"`
	Members   []DeploymentSetMember `json:"members"`
}

// DeploymentSetMember is one guest of a set, keyed by its role (ADR-0030:
// "server" | "agent"). Status is the member's ownership status (pending | active
// | tombstoned) — the per-member honesty the set aggregates. Connection is the
// reachable host[:port] once known; never a secret.
type DeploymentSetMember struct {
	Role       string `json:"role"`
	VMID       int    `json:"vmid"`
	Name       string `json:"name,omitempty"`
	Node       string `json:"node"`
	GuestType  string `json:"guestType"`
	Status     string `json:"status"`
	Connection string `json:"connection,omitempty"`
}

// DeploymentSetList is the sets gallery payload.
type DeploymentSetList struct {
	Sets []DeploymentSet `json:"sets"`
}

// CreateSetRequest is the body of POST /tenants/{tenantId}/deployment-sets. It
// provisions a kind:set catalog service (ADR-0029). The server member takes a
// STATIC IP (ServerIP) so the join URL is known before any guest boots (ADR-0030);
// agents get DHCP. AgentCount is clamped to the service role's [min,max]; zero
// falls back to the role default. VMIDs are operator-chosen: ServerVMID for the
// control plane, AgentVMIDs for the workers (len must equal the resolved agent
// count). No credential is collected — the cluster join token is generated.
type CreateSetRequest struct {
	ServiceId string `json:"serviceId"`
	ProjectId string `json:"projectId"`
	// Name is the set's base name; members are named <name>-server and
	// <name>-agent-N.
	Name string `json:"name"`
	Node string `json:"node"`

	Storage  string `json:"storage"`
	Bridge   string `json:"bridge"`
	VLANTag  int    `json:"vlanTag,omitempty"`
	Firewall bool   `json:"firewall"`

	SSHKeys []string `json:"sshKeys,omitempty"`
	Tags    []string `json:"tags,omitempty"`

	// AgentCount is the number of worker nodes (0 → the service role default).
	AgentCount int `json:"agentCount,omitempty"`
	// ServerVMID + AgentVMIDs are the operator-chosen VMIDs. len(AgentVMIDs) must
	// equal the resolved agent count.
	ServerVMID int   `json:"serverVmid"`
	AgentVMIDs []int `json:"agentVmids"`

	// ServerIP is the static control-plane address (required for a joinable
	// cluster, ADR-0030): a CIDR + gateway free on the LAN.
	ServerIP *IPConfig `json:"serverIp"`
}

// CreateSetResponse acknowledges an accepted set provision. It carries the set id
// and the resolved members (VMIDs + roles) so the UI can track each. No secret is
// ever returned — the generated cluster token exists only inside the members'
// rendered snippets (ADR-0030).
type CreateSetResponse struct {
	SetID   string                `json:"setId"`
	Status  string                `json:"status"`
	Members []DeploymentSetMember `json:"members"`
}

// SetActionResponse acknowledges a set start/stop: the per-member Proxmox task
// refs the UI polls to completion.
type SetActionResponse struct {
	SetID string    `json:"setId"`
	Tasks []TaskRef `json:"tasks"`
}
