package server

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mrz1836/hush/internal/config"
)

// TestWriteTimeoutExceedsEveryValidApprovalWindow is the invariant that
// matters: for every claim_approval_timeout the config package will accept,
// the write timeout must outlast it.
//
// Regression: WriteTimeout was a hardcoded 90s while claim_approval_timeout
// is configurable to 10m. A host running the 10m setting killed the
// connection 90s into the approval wait; the approver then tapped, the
// claim minted, and the token was burned on a connection that no longer
// existed. The client saw only "EOF" and the server logged "claim approved",
// so neither side reported a failure.
func TestWriteTimeoutExceedsEveryValidApprovalWindow(t *testing.T) {
	t.Parallel()

	for approval := config.MinClaimApprovalTimeout; approval <= config.MaxClaimApprovalTimeout; approval += time.Second {
		got := writeTimeoutFor(approval)
		require.Greater(t, got, approval,
			"write timeout %s must exceed claim approval window %s, or a slow "+
				"approval EOFs the client and burns the minted token", got, approval)
	}
}

func TestWriteTimeoutFor(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		approval time.Duration
		want     time.Duration
	}{
		{
			name:     "unset config falls back to the hardening default",
			approval: 0,
			want:     DefaultWriteTimeout,
		},
		{
			name:     "negative is treated as unset",
			approval: -1 * time.Second,
			want:     DefaultWriteTimeout,
		},
		{
			name:     "short window keeps the default floor",
			approval: 10 * time.Second,
			want:     DefaultWriteTimeout,
		},
		{
			name:     "package default (60s) still fits under the floor",
			approval: config.DefaultClaimApprovalTimeout,
			want:     DefaultWriteTimeout,
		},
		{
			name:     "the window that used to EOF now gets headroom",
			approval: 10 * time.Minute,
			want:     10*time.Minute + ClaimApprovalWriteHeadroom,
		},
		{
			name:     "just past the floor grows instead of clamping",
			approval: DefaultWriteTimeout,
			want:     DefaultWriteTimeout + ClaimApprovalWriteHeadroom,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, writeTimeoutFor(tc.approval))
		})
	}
}

// TestWriteTimeoutHeadroomCoversPostApprovalWork documents why the margin
// exists at all: the handler still mints, writes an audit record, and
// encodes a response after the approver taps.
func TestWriteTimeoutHeadroomCoversPostApprovalWork(t *testing.T) {
	t.Parallel()

	require.Positive(t, ClaimApprovalWriteHeadroom,
		"headroom must be positive; an exact-fit write timeout races the mint")
}
