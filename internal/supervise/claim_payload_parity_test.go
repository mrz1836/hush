package supervise

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mrz1836/hush/internal/testutil"
)

// TestClaimSignedPayloadMatchesServer pins the supervisor's signed payload
// to the authoritative field set. sign.CanonicalJSON emits every exported
// field regardless of omitempty, so drift from the server's signedPayload
// makes every supervised claim fail bad_signature — which strands the
// supervised child without its secrets.
func TestClaimSignedPayloadMatchesServer(t *testing.T) {
	t.Parallel()

	got := testutil.CanonicalFieldNames(claimSignedPayload{})

	require.Equal(t, testutil.ClaimSignedPayloadFields(), got,
		"supervise.claimSignedPayload drifted from the /claim signed payload field set; "+
			"every supervisor claim will fail bad_signature until the field sets match")
}
