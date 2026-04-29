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

	"github.com/golang-jwt/jwt/v5"
)

// Validate parses and verifies an encoded ES256K JWT, then runs the
// deterministic validation ordering: ctx → header alg → signature +
// expiry → session-type vocabulary → scope → client IP → store
// revocation → use-count decrement (interactive only). Each rejection
// surfaces a distinct sentinel; on success the recovered *Claims is
// returned.
func Validate(
	ctx context.Context,
	encoded string,
	verifyKey *ecdsa.PublicKey,
	store Store,
	requestIP string,
	requestedSecret string,
) (*Claims, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	Register()

	if err := validateAlgorithm(encoded); err != nil {
		return nil, err
	}

	keyfunc := func(*jwt.Token) (any, error) { return verifyKey, nil }
	parsed, err := jwt.ParseWithClaims(
		encoded,
		&Claims{},
		keyfunc,
		jwt.WithValidMethods([]string{"ES256K"}),
	)
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, ErrTokenExpired
		}
		return nil, ErrAlgorithmUnsupported
	}

	claims := parsed.Claims.(*Claims)

	if !validSessionType(claims.SessionType) {
		return nil, ErrUnknownSessionType
	}

	if !slices.Contains(claims.Scope, requestedSecret) {
		return nil, ErrScopeViolation
	}

	if err := compareIPs(claims.ClientIP, requestIP); err != nil {
		return nil, err
	}

	if _, err := store.Get(claims.ID); err != nil {
		return nil, err
	}

	if err := store.ConsumeUse(claims.ID); err != nil {
		return nil, err
	}

	return claims, nil
}

func validateAlgorithm(encoded string) error {
	dot := strings.Index(encoded, ".")
	if dot < 0 {
		return ErrAlgorithmUnsupported
	}
	headerJSON, err := base64.RawURLEncoding.DecodeString(encoded[:dot])
	if err != nil {
		return ErrAlgorithmUnsupported
	}
	var hdr struct {
		Alg string `json:"alg"`
	}
	if err := json.Unmarshal(headerJSON, &hdr); err != nil {
		return ErrAlgorithmUnsupported
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
