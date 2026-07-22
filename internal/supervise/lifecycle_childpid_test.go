package supervise

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestLifecycleChildPID_SnapshotTracksLiveChild verifies the status Store
// reports the live child's PID in Snapshot (and therefore the status socket's
// `child_pid` field). The Store.childPID field previously had no setter, so
// every status read rendered `child_pid: null` even while a child ran. The
// orchestrator now records the PID on Child.Start and clears it on exit; this
// test locks in both the initial set and the update across a restart-swap.
func TestLifecycleChildPID_SnapshotTracksLiveChild(t *testing.T) {
	tl := newTestLifecycle(t, longChildCmd())
	cancel, done := runUntilRunning(t, tl)
	defer shutdownLifecycle(t, cancel, done)

	bootPID := currentChildPID(t, tl)
	require.Greater(t, bootPID, 0, "child should be running after boot")
	require.Equal(t, bootPID, tl.lc.store.Snapshot().ChildPID,
		"snapshot must report the live child PID after boot, not 0/null")

	// A restart-renew stops the old child (clears the PID) and starts a fresh
	// one (records the new PID); the snapshot must follow to the new child.
	tl.vault.QueueOK()
	res, err := dispatchRenewForTest(t, tl, RenewRequest{Restart: true})
	require.NoError(t, err)
	require.True(t, res.Restarted)

	eventually(t, "snapshot child_pid follows the restarted child", 5*time.Second, func() bool {
		newPID := currentChildPID(t, tl)
		return newPID > 0 && newPID != bootPID && tl.lc.store.Snapshot().ChildPID == newPID
	})
}
