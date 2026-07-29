package cli

import (
	"crypto/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mrz1836/hush/internal/testutil"
	"github.com/mrz1836/hush/internal/transport/sign"
)

// TestClaimSignedPayloadMatchesServer pins the interactive client's signed
// payload to the authoritative field set. sign.CanonicalJSON emits every
// exported field regardless of omitempty, so any field the server declares
// and this struct omits changes the signed bytes and makes the server
// reject every interactive claim with bad_signature.
//
// Regression: standing_lease and client_machine_index were added to
// internal/server (and to internal/supervise and the integration harness)
// but not here, which broke every interactive `hush request` host-wide
// while the whole test suite stayed green.
func TestClaimSignedPayloadMatchesServer(t *testing.T) {
	t.Parallel()

	got := testutil.CanonicalFieldNames(claimSignedPayload{})

	require.Equal(t, testutil.ClaimSignedPayloadFields(), got,
		"claimSignedPayload drifted from the /claim signed payload field set; "+
			"every interactive claim will fail bad_signature until the field sets match")
}

// TestServerStandInPayloadMatchesServer guards the stand-in this package's
// tests use in place of internal/server's unexported signedPayload.
//
// A stand-in that drifts in the same direction as the client it checks
// makes the two errors cancel: the signature verifies against a wrong
// shape and the test passes while the real server rejects every claim.
// The stand-in must therefore track the server, never the client.
func TestServerStandInPayloadMatchesServer(t *testing.T) {
	t.Parallel()

	got := testutil.CanonicalFieldNames(serverClaimSignedPayloadForTest{})

	require.Equal(t, testutil.ClaimSignedPayloadFields(), got,
		"the server stand-in drifted from the /claim signed payload field set; "+
			"it can no longer prove client signatures verify against the real server")
}

// TestClaimSignedPayloadCanonicalIncludesSupervisorFields is the narrow
// regression guard for the drift above: the supervisor-only fields must
// appear in the canonical bytes of an ordinary interactive payload, at
// their zero values, because the server canonicalises them from an
// absent wire field (zero) on every claim.
func TestClaimSignedPayloadCanonicalIncludesSupervisorFields(t *testing.T) {
	t.Parallel()

	canonical, err := sign.CanonicalJSON(claimSignedPayload{
		EphemeralPubKey: "02ab",
		MachineName:     "mac",
		Nonce:           "n",
		Reason:          "r",
		RequestID:       "rid",
		Scope:           []string{"TEST"},
		SessionType:     "interactive",
		Timestamp:       "2026-07-29T00:00:00Z",
		TTL:             "1h0m0s",
	})
	require.NoError(t, err)

	require.Contains(t, string(canonical), `"client_machine_index":0`)
	require.Contains(t, string(canonical), `"standing_lease":false`)
}

// TestBuildClaimPayloadLeavesSupervisorFieldsZero guards the inverse
// mistake. The --machine-index flag selects the keychain item
// (hush-client/machine-N) and is never placed on this client's wire
// envelope, so the server always verifies against zero. Assigning the
// flag to the signed payload would look like a fix and would instead
// break every machine whose index is not 0.
func TestBuildClaimPayloadLeavesSupervisorFieldsZero(t *testing.T) {
	t.Parallel()

	deps := requestDeps{
		nowFn:      func() time.Time { return time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC) },
		randReader: rand.Reader,
		hostnameFn: func() (string, error) { return "mac", nil },
	}
	flags := requestFlags{
		scope:        []string{"TEST"},
		reason:       "parity check",
		ttl:          time.Hour,
		machineIndex: 7, // non-zero on purpose: must NOT reach the payload
	}

	payload, err := buildClaimPayload(flags, "02ab", deps)
	require.NoError(t, err)

	require.Zero(t, payload.ClientMachineIndex,
		"client_machine_index must stay zero: --machine-index selects the keychain "+
			"item and is not sent on the wire, so the server verifies against zero")
	require.False(t, payload.StandingLease,
		"standing_lease is supervisor-only and is rejected on an interactive claim")
}
