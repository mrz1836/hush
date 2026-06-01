package supervise

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/mrz1836/hush/internal/supervise/config"
)

// TestZeroBytes covers V5: the small helper used in doClaimRequest and
// refill.fetchOne to scrub a response body after parse/decrypt. The
// helper does NOT close the residual-risk #12 unzeroable-string gap on
// json.Unmarshal'd fields — it just drops one of the two unscrubbed
// heap copies. Verifying the helper itself is enough; the deferred call
// site in doClaimRequest is one line and visually auditable.
func TestZeroBytes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   []byte
		want []byte
	}{
		{"nil", nil, nil},
		{"empty", []byte{}, []byte{}},
		{"single_byte", []byte{0xff}, []byte{0x00}},
		{"multi_byte", []byte("eyJhbGciOiJFUzI1NksifQ.placeholder.sig"), make([]byte, len("eyJhbGciOiJFUzI1NksifQ.placeholder.sig"))},
		{"already_zero", make([]byte, 16), make([]byte, 16)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			zeroBytes(tc.in)
			if !bytes.Equal(tc.in, tc.want) {
				t.Errorf("after zeroBytes: got %x, want %x", tc.in, tc.want)
			}
		})
	}
}

// TestZeroBytes_NoPanicOnNil documents that zeroBytes must be safe to
// call on a nil slice — the deferred zeroBytes in doClaimRequest fires
// even on early-return paths where io.ReadAll never populated the slice.
func TestZeroBytes_NoPanicOnNil(t *testing.T) {
	t.Parallel()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("zeroBytes(nil) panicked: %v", r)
		}
	}()
	zeroBytes(nil)
}

func TestBuildClaimPayload_UsesStaticTTLWithoutReseal(t *testing.T) {
	tl := newTestLifecycle(t, longChildCmd(), func(cfg *config.Supervisor) {
		cfg.RequestedTTL = 17 * time.Hour
	})
	tl.lc.deps.NowFn = func() time.Time { return time.Date(2026, 1, 2, 8, 0, 0, 0, time.UTC) }
	tl.lc.deps.NonceFn = func() string { return "nonce" }
	tl.lc.deps.RequestIDFn = func() string { return "request-id" }

	payload := tl.lc.buildClaimPayload()
	if payload.TTL != "17h0m0s" {
		t.Fatalf("TTL=%q want %q", payload.TTL, "17h0m0s")
	}
}

func TestSubmitClaim_UsesScheduledResealTTL(t *testing.T) {
	now := time.Date(2026, 1, 2, 8, 0, 0, 0, time.UTC)
	tl := newTestLifecycle(t, longChildCmd(), func(cfg *config.Supervisor) {
		cfg.Reseal = loadTestResealSchedule(t, `[reseal]
timezone = "UTC"
daily_time = "10:00"
`)
	})
	tl.lc.deps.NowFn = func() time.Time { return now }

	tl.vault.QueueOK()
	if err := tl.lc.submitClaim(context.Background()); err != nil {
		t.Fatalf("submitClaim: %v", err)
	}
	if got := tl.vault.LastClaim().TTL; got != "2h0m0s" {
		t.Fatalf("wire TTL=%q want %q", got, "2h0m0s")
	}
}

func TestPerformRefreshClaim_UsesScheduledResealTTL(t *testing.T) {
	now := time.Date(2026, 1, 2, 8, 0, 0, 0, time.UTC)
	tl := newTestLifecycle(t, longChildCmd(), func(cfg *config.Supervisor) {
		cfg.Reseal = loadTestResealSchedule(t, `[reseal]
timezone = "UTC"
daily_time = "10:00"
`)
	})
	tl.lc.deps.NowFn = func() time.Time { return now }

	tl.vault.QueueOK()
	if got := tl.lc.performRefreshClaim(context.Background()); got.err != nil {
		t.Fatalf("performRefreshClaim: %v", got.err)
	}
	if got := tl.vault.LastClaim().TTL; got != "2h0m0s" {
		t.Fatalf("wire TTL=%q want %q", got, "2h0m0s")
	}
}

func TestBuildClaimPayload_LogsClampWhenResealGapExceedsCeiling(t *testing.T) {
	var logs bytes.Buffer
	now := time.Date(2026, 1, 2, 9, 30, 0, 0, time.UTC)
	tl := newTestLifecycle(t, longChildCmd(), func(cfg *config.Supervisor) {
		cfg.Reseal = loadTestResealSchedule(t, `[reseal]
timezone = "UTC"
daily_time = "10:00"
`)
	})
	tl.lc.deps.NowFn = func() time.Time { return now }
	tl.lc.deps.Logger = slog.New(slog.NewTextHandler(io.MultiWriter(&logs), &slog.HandlerOptions{Level: slog.LevelInfo}))

	payload := tl.lc.buildClaimPayload()
	if payload.TTL != "24h0m0s" {
		t.Fatalf("TTL=%q want %q", payload.TTL, "24h0m0s")
	}
	out := logs.String()
	if !strings.Contains(out, "supervise: reseal schedule clamped to max TTL") {
		t.Fatalf("clamp log missing: %s", out)
	}
	if !strings.Contains(out, "computed_gap=24h30m0s") || !strings.Contains(out, "ceiling=24h0m0s") {
		t.Fatalf("clamp log fields missing: %s", out)
	}
}
