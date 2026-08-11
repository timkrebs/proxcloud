package types

// Pool is one Proxmox resource pool. Members counts every member
// (guests and storages) as reported by the pool detail endpoint.
type Pool struct {
	PoolID  string `json:"poolId"`
	Comment string `json:"comment"`
	Members int    `json:"members"`
}
