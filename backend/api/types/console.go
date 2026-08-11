package types

// ConsoleSession is the answer to POST .../console: a one-shot session id
// for the backend websocket bridge, plus the one-time VNC password (vnc
// only) noVNC uses for the RFB handshake.
type ConsoleSession struct {
	SessionID string `json:"sessionId"`
	Kind      string `json:"kind"` // vnc | term
	Password  string `json:"password,omitempty"`
}
