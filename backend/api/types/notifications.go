package types

import "time"

// Notification is one entry of the bell pane: the durable representation of
// a task Proxcloud started. Kind mirrors the design vocabulary.
type Notification struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"` // prog | ok | err
	Title     string    `json:"title"`
	Detail    string    `json:"detail"`
	UPID      string    `json:"upid,omitempty"`
	Status    string    `json:"status"` // running | succeeded | failed
	CreatedAt time.Time `json:"createdAt"`
	Read      bool      `json:"read"`
}

// MarkReadRequest is the POST /api/notifications/read body.
type MarkReadRequest struct {
	IDs []string `json:"ids"`
}
