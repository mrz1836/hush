package server

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mrz1836/hush/internal/transport/sign"
)

// TestClaim_NonceCacheFull_503 pins the T1 wire contract: when the nonce
// cache returns [sign.ErrNonceCacheFull] (Layer-4 saturation), the claim
// handler MUST return HTTP 503 with errCodeNonceCacheFull and emit a
// dedicated outcomeNonceCacheFull audit event — NEVER conflate it with
// the 403 nonce_replay path, which would let saturation hide as replay
// (Constitution VI).
func TestClaim_NonceCacheFull_503(t *testing.T) {
	t.Parallel()
	h := newClaimHarness(
		t,
		withDepsMutator(func(d *Deps) {
			// Capacity-1 cache so the SECOND distinct nonce triggers the
			// saturation path immediately. No timer-driven sweep races —
			// newCappedNonceCacheForTest in sign uses a 1-hour sweep
			// interval, so any cap behavior we observe is purely from
			// the synchronous Add path.
			d.NonceCache = sign.NewNonceCacheWithCap(1)
		}),
		withApproverScript(
			// First request would succeed if it reached the approver; the
			// second request is what we want to verify, so script two
			// approvals defensively.
			[]Decision{
				{Approved: true, GrantedTTL: time.Hour, ApproverID: "test"},
				{Approved: true, GrantedTTL: time.Hour, ApproverID: "test"},
			},
			[]error{nil, nil},
		),
	)

	// First request consumes the single capacity slot.
	o1 := defaultClaimBodyOpts(h)
	rr1, _ := h.do(t, signedClaimBody(t, h, o1))
	if rr1.Code != http.StatusOK {
		t.Fatalf("first claim: status=%d body=%s want 200", rr1.Code, rr1.Body.String())
	}

	// Second request with a DISTINCT nonce — must hit cap, must return 503.
	o2 := defaultClaimBodyOpts(h)
	rr2, _ := h.do(t, signedClaimBody(t, h, o2))
	if rr2.Code != http.StatusServiceUnavailable {
		t.Fatalf("second claim under saturation: status=%d body=%s want 503",
			rr2.Code, rr2.Body.String())
	}
	assertErrorBodyShape(t, rr2, "nonce_cache_full")

	// Audit must carry the dedicated outcome label.
	var sawCacheFull bool
	for _, e := range h.auditEvents() {
		if e.Detail["outcome"] == string(outcomeNonceCacheFull) {
			sawCacheFull = true
			break
		}
	}
	if !sawCacheFull {
		t.Fatalf("no %s audit event recorded: %+v",
			outcomeNonceCacheFull, h.auditEvents())
	}

	// Ops log MUST carry the loud failure (Constitution VI). It writes at
	// ERROR with the literal outcome label.
	if got := h.slogBuf.String(); !strings.Contains(got, string(outcomeNonceCacheFull)) {
		t.Errorf("expected ops log to mention %q, got: %s",
			outcomeNonceCacheFull, got)
	}
}

// TestClaim_NonceCacheFullDoesNotMaskReplay pins the ordering invariant the
// sign-package test already covers at the cache level: even when the cache
// is saturated, a replay of an already-cached nonce MUST resolve as
// nonce_replay (403), NOT nonce_cache_full (503). This is the wire-level
// regression guard — if the handler ever inverts the switch ordering,
// operators lose the ability to distinguish "we are under replay attack"
// from "we are under flood".
func TestClaim_NonceCacheFullDoesNotMaskReplay(t *testing.T) {
	t.Parallel()
	h := newClaimHarness(
		t,
		withDepsMutator(func(d *Deps) {
			d.NonceCache = sign.NewNonceCacheWithCap(1)
		}),
		withApproverScript(
			[]Decision{{Approved: true, GrantedTTL: time.Hour, ApproverID: "test"}},
			[]error{nil},
		),
	)

	// First call wins the only slot AND seeds the nonce.
	o := defaultClaimBodyOpts(h)
	body := signedClaimBody(t, h, o)
	rr1, _ := h.do(t, body)
	if rr1.Code != http.StatusOK {
		t.Fatalf("first claim: status=%d want 200", rr1.Code)
	}

	// Replay the SAME nonce. Cache is saturated AND the nonce is already
	// resident → must hit the replay path (403), not the cache-full path.
	rr2, _ := h.do(t, body)
	if rr2.Code != http.StatusForbidden {
		t.Fatalf("replay at saturation: status=%d want 403 (replay must win)", rr2.Code)
	}
	assertErrorBodyShape(t, rr2, "nonce_replay")
}
