package token

import "errors"

// Sentinel errors. Compare via errors.Is. Messages are static and
// embed no JWT, signing-key, or verify-key bytes.
//
// Granularity follows the principle that operators should be able to alert
// on each failure class independently: an issuance call that fails because
// the caller passed bad params is operationally distinct from one that
// failed because the OS RNG returned an error, which is distinct from a
// crypto-level signing failure. Keep new errors as their own sentinel; do
// not collapse classes for brevity.
var (
	// ErrAlgorithmUnsupported reports that the token header's "alg" claim
	// is not the single supported value (ES256K). Use only for the actual
	// alg-mismatch case; other malformed-token rejections use ErrTokenMalformed.
	ErrAlgorithmUnsupported = errors.New("hush/token: algorithm unsupported")
	// ErrTokenExpired reports that the token's exp claim has passed.
	ErrTokenExpired = errors.New("hush/token: token expired")
	// ErrTokenRevoked reports that the token's jti is in the revoked set.
	ErrTokenRevoked = errors.New("hush/token: token revoked")
	// ErrTokenExhausted reports that the interactive session has consumed
	// all of its max_uses budget.
	ErrTokenExhausted = errors.New("hush/token: token exhausted")
	// ErrIPMismatch reports that the requesting IP differs from the
	// claim's client_ip.
	ErrIPMismatch = errors.New("hush/token: ip mismatch")
	// ErrScopeViolation reports that the requested secret is not in the
	// claim's scope array.
	ErrScopeViolation = errors.New("hush/token: scope violation")
	// ErrUnknownSessionType reports that the session_type claim is not
	// one of the known SessionType values.
	ErrUnknownSessionType = errors.New("hush/token: unknown session type")
	// ErrInvalidIssueParams reports that one or more fields supplied via
	// IssueParams violated their pre-condition (zero/empty/malformed).
	// Operationally this is a caller bug, distinct from a crypto failure.
	ErrInvalidIssueParams = errors.New("hush/token: invalid issue params")
	// ErrJTIGeneration reports that the OS random source returned an
	// error while generating a JTI. Operationally this is an entropy
	// starvation / kernel issue, distinct from a caller bug.
	ErrJTIGeneration = errors.New("hush/token: jti generation failed")
	// ErrSigningFailed reports that signing the JWT with the supplied
	// private key returned an error. Operationally this is a key-handle
	// or crypto-runtime issue, distinct from a caller bug.
	ErrSigningFailed = errors.New("hush/token: signing failed")
	// ErrTokenMalformed reports that the encoded JWT could not be parsed
	// (no separator, bad base64, bad JSON, missing claims). Distinct from
	// ErrAlgorithmUnsupported (alg-claim mismatch) and ErrSignatureInvalid
	// (verify failed).
	ErrTokenMalformed = errors.New("hush/token: token malformed")
	// ErrSignatureInvalid reports that the JWT signature did not verify
	// against the supplied public key. Distinct from ErrTokenMalformed
	// (parsing failure before verification was attempted).
	ErrSignatureInvalid = errors.New("hush/token: signature invalid")
	// ErrInvalidIssuer reports that the token's iss claim is not the
	// fixed hush issuer label. Defends against key-reuse mistakes: a
	// future protocol that signs JWTs with the same key would otherwise
	// produce tokens this validator silently accepts.
	ErrInvalidIssuer = errors.New("hush/token: invalid issuer")
)
