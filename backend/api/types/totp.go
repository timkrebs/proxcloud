package types

// EnrollTOTPResponse is the POST /api/auth/totp/enroll response. The shared
// secret is generated, AES-256-GCM sealed, and stored UNCONFIRMED server-side;
// nothing here reveals it in plaintext beyond the standard otpauth secret the
// user keys into their authenticator app.
type EnrollTOTPResponse struct {
	OtpauthURI   string `json:"otpauthUri"`   // otpauth://totp/Issuer:email?secret=…&issuer=Issuer
	QrPngDataUri string `json:"qrPngDataUri"` // "data:image/png;base64,…" — server-rendered QR
	ManualKey    string `json:"manualKey"`    // base32 secret, for manual authenticator entry
}

// VerifyEnrollRequest is the POST /api/auth/totp/verify body — a 6-digit code
// from the authenticator that proves possession of the pending secret.
type VerifyEnrollRequest struct {
	Code string `json:"code"`
}

// VerifyEnrollResponse is returned ONCE, at enable, carrying the 10 plaintext
// recovery codes. They are never retrievable again (only unused counts appear in Me).
type VerifyEnrollResponse struct {
	RecoveryCodes []string `json:"recoveryCodes"` // 10 × "XXXXX-XXXXX"
}

// PasswordConfirmRequest re-prompts the caller's password before a sensitive
// account mutation (TOTP disable, recovery-code regeneration).
type PasswordConfirmRequest struct {
	Password string `json:"password"`
}

// LoginTOTPRequest is the POST /api/auth/login/totp body. Code is a 6-digit TOTP
// OR a recovery code ("XXXXX-XXXXX"); the handler auto-detects by shape.
type LoginTOTPRequest struct {
	Code string `json:"code"`
}

// RecoveryCodesResponse is returned ONCE, at regenerate, carrying the 10 new
// plaintext recovery codes. Never retrievable again.
type RecoveryCodesResponse struct {
	RecoveryCodes []string `json:"recoveryCodes"` // 10 × "XXXXX-XXXXX"
}
