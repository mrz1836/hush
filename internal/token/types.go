package token

import (
	"fmt"
	"net/netip"
	"sort"
)

// ClientIP is the canonical netip-string form of a client IP address.
// Two ClientIP values compare equal via == iff they represent the same
// address — IPv6 short ("::1") and long ("0000:...:0001") forms collapse
// to the same canonical string. Constructed via [NewClientIP] from a
// validated *netip.Addr, or via [ParseClientIP] from an untrusted string.
//
// The named type exists to make the canonical-form contract enforceable
// at function signatures: a parameter declared `clientIP ClientIP` cannot
// silently receive a non-canonical string from an upstream layer that
// forgot to round-trip through netip.
type ClientIP string

// Scope is a sorted, deduplicated set of scope names. The sort order is
// part of the contract — two Scopes whose elements differ only in order
// (or in duplicates) compare equal via [slices.Equal] of the underlying
// slice. Construct via [NewScope]; do not build literals.
type Scope []string

// NewClientIP returns the canonical-form ClientIP for addr. addr MUST be
// a valid (non-zero) netip.Addr; the function returns ClientIP("") for
// the zero value.
func NewClientIP(addr netip.Addr) ClientIP {
	if !addr.IsValid() {
		return ""
	}
	return ClientIP(addr.String())
}

// ParseClientIP parses s as an IP address (IPv4 or IPv6) and returns its
// canonical-form ClientIP, or a wrapped parse error.
func ParseClientIP(s string) (ClientIP, error) {
	addr, err := netip.ParseAddr(s)
	if err != nil {
		return "", fmt.Errorf("token: parse client ip: %w", err)
	}
	return ClientIP(addr.String()), nil
}

// NewScope returns the canonical-form Scope for in: a fresh slice
// containing each unique non-empty element of in, sorted lexicographically.
// Empty input yields a nil Scope.
//
// The function is idempotent on its own output: NewScope(NewScope(x)) is
// byte-equal to NewScope(x).
func NewScope(in []string) Scope {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" {
			continue
		}
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil
	}
	sort.Strings(out)
	// Dedupe in place — out is sorted so duplicates are adjacent.
	j := 0
	for i := 1; i < len(out); i++ {
		if out[i] != out[j] {
			j++
			out[j] = out[i]
		}
	}
	return Scope(out[:j+1])
}
