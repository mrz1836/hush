package token

import "errors"

// Sentinel errors. Compare via errors.Is. Messages are static and
// embed no JWT, signing-key, or verify-key bytes (FR-014).
var (
	ErrAlgorithmUnsupported = errors.New("hush/token: algorithm unsupported")
	ErrTokenExpired         = errors.New("hush/token: token expired")
	ErrTokenRevoked         = errors.New("hush/token: token revoked")
	ErrTokenExhausted       = errors.New("hush/token: token exhausted")
	ErrIPMismatch           = errors.New("hush/token: ip mismatch")
	ErrScopeViolation       = errors.New("hush/token: scope violation")
	ErrUnknownSessionType   = errors.New("hush/token: unknown session type")
)
