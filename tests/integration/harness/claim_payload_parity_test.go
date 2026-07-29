//go:build integration

package harness

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mrz1836/hush/internal/testutil"
)

// TestClaimSignedPayloadJSONMatchesServer pins the harness's signed payload
// to the authoritative field set.
//
// This copy carries a specific hazard: the harness signs its own claims, so
// when it is correct and a production client is not, the integration suite
// passes while that client is broken in the field. That is exactly how the
// standing_lease / client_machine_index drift in internal/cli survived —
// green integration runs the whole time. Keeping this struct honest is what
// makes the suite's green meaningful.
func TestClaimSignedPayloadJSONMatchesServer(t *testing.T) {
	t.Parallel()

	got := testutil.CanonicalFieldNames(claimSignedPayloadJSON{})

	require.Equal(t, testutil.ClaimSignedPayloadFields(), got,
		"harness claimSignedPayloadJSON drifted from the /claim signed payload field set; "+
			"the integration suite can no longer detect client drift")
}
