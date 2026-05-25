package supervise

// P2 (supervise side): buildClaimPayload must populate SupervisorName
// from the supervisor's [name] config field. The server requires this
// for session_type=supervisor — without it, every supervisor claim
// would be rejected as bad_request post-finding-P2 enforcement.

import (
	"strings"
	"testing"
)

// TestBuildClaimPayload_PopulatesSupervisorName — the orchestrator
// must thread cfg.Name through to the signed payload so the server's
// shape validator accepts the claim and so the Discord prompt
// renders the supervisor label.
func TestBuildClaimPayload_PopulatesSupervisorName(t *testing.T) {
	t.Parallel()
	tl := newTestLifecycle(t, longChildCmd())
	pl := tl.lc.buildClaimPayload()
	if pl.SupervisorName != tl.cfg.Name {
		t.Errorf("payload.SupervisorName=%q want %q (cfg.Name)", pl.SupervisorName, tl.cfg.Name)
	}
	if pl.SupervisorName == "" {
		t.Fatal("buildClaimPayload produced an empty SupervisorName; server will 400")
	}
	if pl.SessionType != "supervisor" {
		t.Errorf("payload.SessionType=%q want supervisor", pl.SessionType)
	}
}

// TestBuildClaimPayload_SupervisorNamePropagatesToWire — the wrapped
// wire envelope carries the SupervisorName so the server can
// reconstruct the signed payload and the canonical-JSON bytes match.
func TestBuildClaimPayload_SupervisorNamePropagatesToWire(t *testing.T) {
	t.Parallel()
	tl := newTestLifecycle(t, longChildCmd())
	pl := tl.lc.buildClaimPayload()

	wire, err := signAndWrapClaim(t.Context(), tl.lc.deps.ClaimSigningKey, "fp01020304050607", pl)
	if err != nil {
		t.Fatalf("signAndWrapClaim: %v", err)
	}
	if wire.SupervisorName != pl.SupervisorName {
		t.Errorf("wire.SupervisorName=%q want %q", wire.SupervisorName, pl.SupervisorName)
	}
	if !strings.HasPrefix(wire.SessionType, "supervisor") {
		t.Errorf("wire.SessionType=%q want supervisor", wire.SessionType)
	}
}
