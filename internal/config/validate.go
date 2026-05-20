package config

import (
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"sync"
)

// pathPrefixRegex is initialized once via sync.Once (no init()). Constitution IX.
var (
	pathPrefixOnce  sync.Once      //nolint:gochecknoglobals // sentinel-class: set-once, lazy init via sync.Once
	pathPrefixRegex *regexp.Regexp //nolint:gochecknoglobals // sentinel-class: set-once, lazy init via sync.Once
)

func getPathPrefixRegex() *regexp.Regexp {
	pathPrefixOnce.Do(func() {
		pathPrefixRegex = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	})
	return pathPrefixRegex
}

// Validate runs all documented validation rules against s and returns a
// joined error if one or more rules fail.
//
// Rule order (locked for determinism):
//  1. require_tailscale gate
//  2. Argon2id floors (memory, time, threads), then ceilings (same order)
//  3. listen_addr family
//  4. health_bind family (when explicitly set)
//  5. path_prefix
//  6. audit_log containment
//  7. max_supervisor_ttl bounds
func (s *Server) Validate() error { //nolint:cyclop,gocognit,gocyclo // rule-engine: one function per locked rule order
	var errs []error

	// 1. require_tailscale gate
	if !s.Network.RequireTailscale {
		errs = append(errs, fmt.Errorf("field require_tailscale: %w", ErrTailscaleRequired))
	}

	// 2a. Argon2id floors — Constitution III
	if s.Crypto.ArgonMemoryMB < MinArgonMemoryMB {
		errs = append(errs, fmt.Errorf("field argon_memory_mb=%d: %w", s.Crypto.ArgonMemoryMB, ErrArgonMemoryTooLow))
	}
	if s.Crypto.ArgonTime < MinArgonTime {
		errs = append(errs, fmt.Errorf("field argon_time=%d: %w", s.Crypto.ArgonTime, ErrArgonTimeTooLow))
	}
	if s.Crypto.ArgonThreads < MinArgonThreads {
		errs = append(errs, fmt.Errorf("field argon_threads=%d: %w", s.Crypto.ArgonThreads, ErrArgonThreadsTooLow))
	}

	// 2b. Argon2id ceilings — DoS-via-config prevention (H3).
	if s.Crypto.ArgonMemoryMB > MaxArgonMemoryMB {
		errs = append(errs, fmt.Errorf("field argon_memory_mb=%d: %w", s.Crypto.ArgonMemoryMB, ErrArgonMemoryTooHigh))
	}
	if s.Crypto.ArgonTime > MaxArgonTime {
		errs = append(errs, fmt.Errorf("field argon_time=%d: %w", s.Crypto.ArgonTime, ErrArgonTimeTooHigh))
	}
	if s.Crypto.ArgonThreads > MaxArgonThreads {
		errs = append(errs, fmt.Errorf("field argon_threads=%d: %w", s.Crypto.ArgonThreads, ErrArgonThreadsTooHigh))
	}

	// 3. listen_addr family
	errs = append(errs, validateListenField("listen_addr", s.rawListenAddr, s.Server.ListenAddr)...)

	// 4. health_bind family — only when explicitly set in TOML
	if s.rawHealthBind != "" {
		errs = append(errs, validateListenField("health_bind", s.rawHealthBind, s.Network.HealthBind)...)
	}

	// 5. path_prefix
	if err := validatePathPrefix(s.Server.PathPrefix); err != nil {
		errs = append(errs, err)
	}

	// 6. audit_log containment
	if s.Server.AuditLog != "" && s.Server.StateDir != "" {
		if !isUnderStateDir(s.Server.AuditLog, s.Server.StateDir) {
			errs = append(errs, fmt.Errorf("field audit_log %q: %w", s.Server.AuditLog, ErrAuditLogEscape))
		}
	}

	// 7. max_supervisor_ttl bounds
	if s.Crypto.MaxSupervisorTTL <= s.Crypto.JWTDefaultTTL || s.Crypto.MaxSupervisorTTL > DefaultSupervisorTTLMax {
		errs = append(errs, fmt.Errorf(
			"field max_supervisor_ttl=%s (jwt_default_ttl=%s, cap=%s): %w",
			s.Crypto.MaxSupervisorTTL, s.Crypto.JWTDefaultTTL, DefaultSupervisorTTLMax,
			ErrSupervisorTTLOutOfRange,
		))
	}

	// 8. claim_approval_timeout bounds — DoS-via-config ceiling.
	if s.Crypto.ClaimApprovalTimeout < MinClaimApprovalTimeout || s.Crypto.ClaimApprovalTimeout > MaxClaimApprovalTimeout {
		errs = append(errs, fmt.Errorf(
			"field claim_approval_timeout=%s (min=%s, max=%s): %w",
			s.Crypto.ClaimApprovalTimeout, MinClaimApprovalTimeout, MaxClaimApprovalTimeout,
			ErrClaimApprovalTimeoutOutOfRange,
		))
	}

	return errors.Join(errs...)
}

// validateListenField checks a raw address string + already-parsed AddrPort for
// one address field (listen_addr or health_bind). Returns a slice of errors.
func validateListenField(field, raw string, ap netip.AddrPort) []error {
	if raw == "" {
		return []error{fmt.Errorf("field %s: %w", field, ErrMissingRequiredField)}
	}
	if ap == (netip.AddrPort{}) {
		return []error{fmt.Errorf("field %s %q: %w", field, raw, ErrListenMalformed)}
	}
	if err := validateTailscaleAddrPort(field, ap); err != nil {
		return []error{err}
	}
	return nil
}

// validateTailscaleAddrPort checks that ap is inside the Tailscale CGNAT range.
// It returns a field-annotated sentinel error on rejection.
func validateTailscaleAddrPort(field string, ap netip.AddrPort) error {
	addr := ap.Addr()
	if addr.IsLoopback() {
		return fmt.Errorf("field %s %q: %w", field, ap, ErrListenLoopback)
	}
	if addr.IsUnspecified() {
		return fmt.Errorf("field %s %q: %w", field, ap, ErrListenUnspecified)
	}
	if !TailscaleCGNAT.Contains(addr) {
		return fmt.Errorf("field %s %q: %w", field, ap, ErrListenPublic)
	}
	return nil
}

// validatePathPrefix returns ErrPathPrefixInvalid when prefix fails the
// length or charset rules.
func validatePathPrefix(prefix string) error {
	if prefix == "" {
		return fmt.Errorf("field path_prefix: %w", ErrMissingRequiredField)
	}
	if len(prefix) < MinPathPrefixLen || len(prefix) > MaxPathPrefixLen {
		return fmt.Errorf("field path_prefix %q (len=%d): %w", prefix, len(prefix), ErrPathPrefixInvalid)
	}
	if !getPathPrefixRegex().MatchString(prefix) {
		return fmt.Errorf("field path_prefix %q: %w", prefix, ErrPathPrefixInvalid)
	}
	return nil
}
