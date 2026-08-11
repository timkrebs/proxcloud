package types

// LoginRequest is the POST /api/auth/login body.
type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// Me is the GET /api/auth/me response.
type Me struct {
	Username string `json:"username"`
}
