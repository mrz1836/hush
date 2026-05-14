package token

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"fmt"
	"io"
	"net/netip"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// tokenIssuer is the fixed value written to and required from the JWT iss
// claim. Centralized so Issue and Validate cannot drift; an operator who
// shares the signing key with another service cannot smuggle in a token
// issued under a different label.
const tokenIssuer = "hush"

// IssueParams carries the issuer-side knobs supplied by the claim
// handler. All fields except MaxUses (for SUPERVISOR) are required.
type IssueParams struct {
	Now             time.Time
	TTL             time.Duration
	Scope           []string
	ClientIP        string
	RequestID       string
	MaxUses         int
	EphemeralPubKey string
	SessionType     SessionType
}

// Token is the in-store record returned by Issue. Encoded is the wire
// form returned to the client; the same record is held by the Store
// after Add for future Validate calls to find.
type Token struct {
	JTI         string
	Encoded     string
	ExpiresAt   time.Time
	SessionType SessionType
	MaxUses     int
}

//nolint:gochecknoglobals // sentinel-class test seam; set-once at package load, replaced only by tests for deterministic JTI
var randReader io.Reader = rand.Reader

func generateJTI() (string, error) {
	var b [16]byte
	if _, err := io.ReadFull(randReader, b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// Issue signs a fresh ES256K JWT carrying the supplied params. The
// caller MUST register the returned Token via Store.Add before
// expecting Validate to accept it.
//
//nolint:gocognit,gocyclo,cyclop // sequential field validation: branching is inherent to the per-field IssueParams contract
func Issue(ctx context.Context, signKey *ecdsa.PrivateKey, params IssueParams) (*Token, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	Register()

	if !validSessionType(params.SessionType) {
		return nil, ErrUnknownSessionType
	}
	if params.Now.IsZero() ||
		params.TTL <= 0 ||
		len(params.Scope) == 0 ||
		params.ClientIP == "" ||
		params.RequestID == "" ||
		params.EphemeralPubKey == "" ||
		signKey == nil {
		return nil, ErrInvalidIssueParams
	}
	for _, s := range params.Scope {
		if s == "" {
			return nil, ErrInvalidIssueParams
		}
	}
	if _, err := netip.ParseAddr(params.ClientIP); err != nil {
		return nil, ErrInvalidIssueParams
	}

	maxUses := params.MaxUses
	switch params.SessionType {
	case SessionSupervisor:
		maxUses = 0
	case SessionInteractive:
		if maxUses <= 0 {
			return nil, ErrInvalidIssueParams
		}
	}

	jti, err := generateJTI()
	if err != nil {
		return nil, ErrJTIGeneration
	}

	expiresAt := params.Now.Add(params.TTL)
	claims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    tokenIssuer,
			IssuedAt:  jwt.NewNumericDate(params.Now),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			ID:        jti,
		},
		Scope:           params.Scope,
		ClientIP:        params.ClientIP,
		RequestID:       params.RequestID,
		MaxUses:         maxUses,
		EphemeralPubKey: params.EphemeralPubKey,
		SessionType:     params.SessionType,
	}

	signed, err := signEncoded(claims, signKey)
	if err != nil {
		return nil, ErrSigningFailed
	}

	return &Token{
		JTI:         jti,
		Encoded:     signed,
		ExpiresAt:   expiresAt,
		SessionType: params.SessionType,
		MaxUses:     maxUses,
	}, nil
}
