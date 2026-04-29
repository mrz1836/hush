package token

import "github.com/golang-jwt/jwt/v5"

// SessionType is the closed vocabulary of session shapes. Adding a
// third value requires a new SDD chunk to define its lifecycle.
type SessionType string

const (
	SessionInteractive SessionType = "interactive"
	SessionSupervisor  SessionType = "supervisor"
)

// Claims is the recovered claim set returned by Validate. It embeds
// jwt.RegisteredClaims for iss/iat/exp/jti and adds the six
// hush-specific fields with stable JSON keys.
type Claims struct {
	jwt.RegisteredClaims

	Scope           []string    `json:"scope"`
	ClientIP        string      `json:"client_ip"`
	RequestID       string      `json:"request_id"`
	MaxUses         int         `json:"max_uses"`
	EphemeralPubKey string      `json:"ephemeral_pubkey"`
	SessionType     SessionType `json:"session_type"`
}

func validSessionType(s SessionType) bool {
	return s == SessionInteractive || s == SessionSupervisor
}
