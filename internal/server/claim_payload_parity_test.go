package server

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mrz1836/hush/internal/testutil"
)

// TestSignedPayloadMatchesAuthoritativeFields pins the server's signed
// payload — the source of truth every client must mirror — to the shared
// field list.
//
// This test failing means the /claim signing contract itself changed.
// That is allowed, but it is never a one-line change: update
// testutil.ClaimSignedPayloadFields and then every client copy
// (internal/cli, internal/supervise, tests/integration/harness), whose
// own parity tests will fail until they match. Shipping the server half
// alone silently breaks the clients that did not follow.
func TestSignedPayloadMatchesAuthoritativeFields(t *testing.T) {
	t.Parallel()

	got := testutil.CanonicalFieldNames(signedPayload{})

	require.Equal(t, testutil.ClaimSignedPayloadFields(), got,
		"the server's signedPayload changed; update testutil.ClaimSignedPayloadFields "+
			"AND every client copy in lockstep, or those clients will fail bad_signature")
}
