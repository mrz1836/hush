package token

import (
	"context"
	"crypto/ecdsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// validateConfig carries the resolved variadic options.
type validateConfig struct {
	clockSkew time.Duration
}

// ValidateOpt mutates validateConfig. Use the With* helpers below.
type ValidateOpt func(*validateConfig)

// WithClockSkew sets the symmetric clock-skew tolerance used when verifying
// the JWT's exp/nbf claims. Caller-supplied values <= 0 are ignored. The
// default (no option, or zero) preserves the historical behavior of zero
// leeway. A typical operational value is 30s.
func WithClockSkew(skew time.Duration) ValidateOpt {
	return func(c *validateConfig) {
		if skew > 0 {
			c.clockSkew = skew
		}
	}
}

// Validate parses and verifies an encoded ES256K JWT, then runs the
// deterministic validation ordering: ctx → header alg → signature +
// expiry → session-type vocabulary → scope → client IP → store
// revocation → use-count decrement (interactive only). Each rejection
// surfaces a distinct sentinel; on success the recovered *Claims is
// returned.
//
// Optional behavior is controlled via ValidateOpt; see WithClockSkew.
func Validate(
	ctx context.Context,
	encoded string,
	verifyKey *ecdsa.PublicKey,
	store Store,
	requestIP string,
	requestedSecret string,
	opts ...ValidateOpt,
) (*Claims, error) {
	var cfg validateConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	Register()

	if err := validateAlgorithm(encoded); err != nil {
		return nil, err
	}

	keyfunc := func(*jwt.Token) (any, error) { return verifyKey, nil }
	parserOpts := []jwt.ParserOption{
		jwt.WithValidMethods([]string{"ES256K"}),
	}
	if cfg.clockSkew > 0 {
		parserOpts = append(parserOpts, jwt.WithLeeway(cfg.clockSkew))
	}
	parsed, err := jwt.ParseWithClaims(
		encoded,
		&Claims{},
		keyfunc,
		parserOpts...,
	)
	if err != nil {
		return nil, mapJWTParseError(err)
	}

	claims := parsed.Claims.(*Claims)
	if err := checkPostParseClaims(claims, requestIP, requestedSecret, store); err != nil {
		return nil, err
	}
	return claims, nil
}

// checkPostParseClaims runs the deterministic post-parse validation chain:
// issuer pin → session-type vocabulary → scope → client IP → store
// revocation → use-count decrement (interactive only). Each rejection
// returns a distinct sentinel; the order is load-bearing for security
// tests.
func checkPostParseClaims(claims *Claims, requestIP, requestedSecret string, store Store) error {
	if claims.Issuer != tokenIssuer {
		return ErrInvalidIssuer
	}
	if !validSessionType(claims.SessionType) {
		return ErrUnknownSessionType
	}
	if !slices.Contains(claims.Scope, requestedSecret) {
		return ErrScopeViolation
	}
	if err := compareIPs(claims.ClientIP, requestIP); err != nil {
		return err
	}
	if _, err := store.Get(claims.ID); err != nil {
		return err
	}
	return store.ConsumeUse(claims.ID)
}

// mapJWTParseError maps jwt.ParseWithClaims errors onto the package's
// distinct sentinels. Anything not specifically signature- or expiry-shaped
// collapses to ErrTokenMalformed.
func mapJWTParseError(err error) error {
	switch {
	case errors.Is(err, jwt.ErrTokenExpired):
		return ErrTokenExpired
	case errors.Is(err, jwt.ErrTokenSignatureInvalid),
		errors.Is(err, jwt.ErrECDSAVerification):
		return ErrSignatureInvalid
	default:
		return ErrTokenMalformed
	}
}

func validateAlgorithm(encoded string) error {
	dot := strings.Index(encoded, ".")
	if dot < 0 {
		return ErrTokenMalformed
	}
	headerJSON, err := base64.RawURLEncoding.DecodeString(encoded[:dot])
	if err != nil {
		return ErrTokenMalformed
	}
	var hdr struct {
		Alg string `json:"alg"`
	}
	if err := json.Unmarshal(headerJSON, &hdr); err != nil {
		return ErrTokenMalformed
	}
	if hdr.Alg != "ES256K" {
		return ErrAlgorithmUnsupported
	}
	return nil
}

func compareIPs(claimIP, requestIP string) error {
	claimed, err := netip.ParseAddr(claimIP)
	if err != nil {
		return ErrIPMismatch
	}
	actual, err := netip.ParseAddr(requestIP)
	if err != nil {
		return ErrIPMismatch
	}
	if claimed != actual {
		return ErrIPMismatch
	}
	return nil
}
