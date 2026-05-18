package setup_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mrz1836/hush/internal/cli/setup"
)

// stubCheck is a deterministic [setup.Check] used by the registry
// tests below. It records the order of Run calls via the shared
// observed slice.
type stubCheck struct {
	name     string
	result   setup.SetupCheckResult
	observed *[]string
	delay    func(ctx context.Context)
}

func (c stubCheck) Name() string { return c.name }
func (c stubCheck) Run(ctx context.Context) setup.SetupCheckResult {
	if c.delay != nil {
		c.delay(ctx)
	}
	if c.observed != nil {
		*c.observed = append(*c.observed, c.name)
	}
	res := c.result
	if res.Name == "" {
		res.Name = c.name
	}
	return res
}

// TestRegistry_RunsInLockedOrder asserts that, regardless of
// registration order, [Registry.Run] walks [setup.CheckOrder]
// front-to-back. AC-2's "deterministic order" promise.
func TestRegistry_RunsInLockedOrder(t *testing.T) {
	t.Parallel()

	var observed []string
	r := setup.NewRegistry()

	// Register the slots in reverse to prove the registry does not
	// honor registration order.
	for i := len(setup.CheckOrder) - 1; i >= 0; i-- {
		name := string(setup.CheckOrder[i])
		require.NoError(t, r.Register(stubCheck{
			name:     name,
			result:   setup.Ok(name, ""),
			observed: &observed,
		}))
	}

	report := r.Run(context.Background())
	require.Len(t, report.Results, len(setup.CheckOrder))
	require.True(t, report.OK())

	want := make([]string, 0, len(setup.CheckOrder))
	for _, n := range setup.CheckOrder {
		want = append(want, string(n))
	}
	require.Equal(t, want, observed, "registry walks CheckOrder front-to-back")
}

// TestRegistry_RegisterRejectsUnknownSlot ensures only documented
// slot names land in the registry.
func TestRegistry_RegisterRejectsUnknownSlot(t *testing.T) {
	t.Parallel()

	r := setup.NewRegistry()
	err := r.Register(stubCheck{name: "tailscale_blunder"})
	require.ErrorIs(t, err, setup.ErrUnknownCheck)
	require.False(t, r.Registered("tailscale_blunder"))
}

// TestRegistry_MustRegisterPanicsOnUnknownSlot covers the
// production wiring panic path. The slot list is constant at
// build time so a typo there is a programmer error.
func TestRegistry_MustRegisterPanicsOnUnknownSlot(t *testing.T) {
	t.Parallel()

	r := setup.NewRegistry()
	require.PanicsWithError(t,
		`hush/setup: unknown check slot: "no_such_check"`,
		func() { r.MustRegister(stubCheck{name: "no_such_check"}) })
}

// TestRegistry_SkipsUnregisteredSlots confirms a partial wiring
// (Phase 1 ships scaffolding; later phases add real Checks)
// produces a report containing only the registered slots in
// CheckOrder.
func TestRegistry_SkipsUnregisteredSlots(t *testing.T) {
	t.Parallel()

	r := setup.NewRegistry()
	require.NoError(t, r.Register(stubCheck{
		name:   string(setup.CheckBinaryVersion),
		result: setup.Ok(string(setup.CheckBinaryVersion), ""),
	}))
	require.NoError(t, r.Register(stubCheck{
		name:   string(setup.CheckClockSync),
		result: setup.Ok(string(setup.CheckClockSync), ""),
	}))

	report := r.Run(context.Background())
	require.Len(t, report.Results, 2)
	require.Equal(t, string(setup.CheckBinaryVersion), report.Results[0].Name)
	require.Equal(t, string(setup.CheckClockSync), report.Results[1].Name)
}

// TestRegistry_StatusBuckets covers the ok / warn / fail per-check
// statuses required by the plan's Phase 1 table-test promise.
// AC-2: every status bucket round-trips through the registry.
func TestRegistry_StatusBuckets(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		result     setup.SetupCheckResult
		wantStatus setup.Status
		wantFail   bool
		wantWarn   bool
		wantOK     bool
	}{
		{
			name:       "ok",
			result:     setup.Ok(string(setup.CheckBinaryVersion), "v1.2.3"),
			wantStatus: setup.StatusOK,
			wantOK:     true,
		},
		{
			name:       "warn",
			result:     setup.Warn(string(setup.CheckBinaryVersion), "old build"),
			wantStatus: setup.StatusWarn,
			wantWarn:   true,
		},
		{
			name:       "fail",
			result:     setup.Fail(string(setup.CheckBinaryVersion), setup.ErrTokenAbsent, "no token"),
			wantStatus: setup.StatusFail,
			wantFail:   true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := setup.NewRegistry()
			require.NoError(t, r.Register(stubCheck{
				name:   string(setup.CheckBinaryVersion),
				result: tc.result,
			}))

			report := r.Run(context.Background())
			require.Len(t, report.Results, 1)
			require.Equal(t, tc.wantStatus, report.Results[0].Status)
			require.Equal(t, tc.wantOK, report.OK())
			if tc.wantWarn {
				require.Len(t, report.Warnings(), 1)
			}
			if tc.wantFail {
				require.NotNil(t, report.FirstFail())
				require.Equal(t, tc.result.Err, report.FirstFail().Err)
				require.NotEmpty(t, report.FirstFail().RemedyHint,
					"fail() should copy RemedyHint from a RemedyHinter error")
			}
		})
	}
}

// TestRegistry_IncompleteCheckIsTreatedAsFail asserts the registry
// catches Checks that forget to populate Status — a programmer
// error rendered as a fail with [ErrCheckIncomplete].
func TestRegistry_IncompleteCheckIsTreatedAsFail(t *testing.T) {
	t.Parallel()

	r := setup.NewRegistry()
	require.NoError(t, r.Register(stubCheck{
		name: string(setup.CheckBinaryVersion),
		// Zero-value SetupCheckResult — Status defaults to
		// StatusUnknown.
		result: setup.SetupCheckResult{},
	}))

	report := r.Run(context.Background())
	require.Len(t, report.Results, 1)
	require.Equal(t, setup.StatusFail, report.Results[0].Status)
	require.ErrorIs(t, report.Results[0].Err, setup.ErrCheckIncomplete)
	require.NotEmpty(t, report.Results[0].RemedyHint)
}

// TestRegistry_CtxCancellationShortCircuits asserts a cancelled
// context skips the remaining checks. The first check observes
// the cancellation and returns; the second is never invoked.
func TestRegistry_CtxCancellationShortCircuits(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	var observed []string

	r := setup.NewRegistry()
	require.NoError(t, r.Register(stubCheck{
		name:     string(setup.CheckBinaryVersion),
		result:   setup.Ok(string(setup.CheckBinaryVersion), ""),
		observed: &observed,
		delay:    func(_ context.Context) { cancel() },
	}))
	require.NoError(t, r.Register(stubCheck{
		name:     string(setup.CheckClockSync),
		result:   setup.Ok(string(setup.CheckClockSync), ""),
		observed: &observed,
	}))

	report := r.Run(ctx)
	require.Equal(t, []string{string(setup.CheckBinaryVersion)}, observed,
		"only the first slot observed before cancellation")
	require.Len(t, report.Results, 1)
}

// TestReport_StringDeterministic asserts [setup.Report.String]
// produces a stable, line-per-check summary suitable for snapshot
// comparison in later phase tests.
func TestReport_StringDeterministic(t *testing.T) {
	t.Parallel()

	r := setup.NewRegistry()
	require.NoError(t, r.Register(stubCheck{
		name:   string(setup.CheckBinaryVersion),
		result: setup.Ok(string(setup.CheckBinaryVersion), "v1.0"),
	}))
	require.NoError(t, r.Register(stubCheck{
		name:   string(setup.CheckClockSync),
		result: setup.Fail(string(setup.CheckClockSync), setup.ErrClockUnsynchronised, "drift 30s"),
	}))

	report := r.Run(context.Background())
	got := report.String()
	require.Equal(t, "binary_version: ok — v1.0\nclock_sync: fail — drift 30s", got)
}

// TestCheckFunc_AdaptsBareFunction covers the [setup.CheckFunc]
// adapter that lets call sites register a plain function as a
// Check without declaring a struct.
func TestCheckFunc_AdaptsBareFunction(t *testing.T) {
	t.Parallel()

	r := setup.NewRegistry()
	require.NoError(t, r.Register(setup.CheckFunc{
		NameValue: string(setup.CheckBinaryVersion),
		RunFn: func(_ context.Context) setup.SetupCheckResult {
			return setup.Ok(string(setup.CheckBinaryVersion), "")
		},
	}))

	report := r.Run(context.Background())
	require.True(t, report.OK())
}

// TestCheckFunc_NilRunFn produces an incomplete result instead of
// panicking — defensive behaviour so tests catch the wiring bug.
func TestCheckFunc_NilRunFn(t *testing.T) {
	t.Parallel()

	c := setup.CheckFunc{NameValue: string(setup.CheckBinaryVersion)}
	res := c.Run(context.Background())
	require.Equal(t, setup.StatusFail, res.Status)
	require.ErrorIs(t, res.Err, setup.ErrCheckIncomplete)
}

// TestStatus_StringLockedTokens asserts the lowercase tokens used
// in the report stay stable. Tests downstream of the registry
// match on these exact strings.
func TestStatus_StringLockedTokens(t *testing.T) {
	t.Parallel()

	require.Equal(t, "ok", setup.StatusOK.String())
	require.Equal(t, "warn", setup.StatusWarn.String())
	require.Equal(t, "fail", setup.StatusFail.String())
	require.Equal(t, "unknown", setup.StatusUnknown.String())
}

// TestRegistry_FailWithoutRemedyHinter confirms a fail result with
// a plain error (no RemedyHint method) leaves RemedyHint empty
// — the rendering layer is responsible for that fallback.
func TestRegistry_FailWithoutRemedyHinter(t *testing.T) {
	t.Parallel()

	plain := errors.New("bare error")
	r := setup.NewRegistry()
	require.NoError(t, r.Register(stubCheck{
		name:   string(setup.CheckBinaryVersion),
		result: setup.Fail(string(setup.CheckBinaryVersion), plain, "boom"),
	}))

	report := r.Run(context.Background())
	require.Empty(t, report.Results[0].RemedyHint)
}

// TestCheckOrder_LockedSlots is a guard test: any future change
// to [setup.CheckOrder] must update the expected slice here. The
// plan's documented order is part of the public contract.
func TestCheckOrder_LockedSlots(t *testing.T) {
	t.Parallel()

	want := []setup.CheckName{
		setup.CheckBinaryVersion,
		setup.CheckConfigTarget,
		setup.CheckStateDir,
		setup.CheckFileModes,
		setup.CheckKeychainReadability,
		setup.CheckTailscaleBind,
		setup.CheckListenPort,
		setup.CheckClockSync,
		setup.CheckArtifactCollision,
	}
	require.Equal(t, want, setup.CheckOrder)
}

// TestRegistry_WrappedSentinelMatches confirms an err that wraps a
// setup sentinel via fmt.Errorf still satisfies errors.Is — the
// guided flow branches on the bare sentinels, never on the wrapped
// instance, so this property must hold.
func TestRegistry_WrappedSentinelMatches(t *testing.T) {
	t.Parallel()

	wrapped := fmt.Errorf("%w: detail", setup.ErrBindConflict)
	r := setup.NewRegistry()
	require.NoError(t, r.Register(stubCheck{
		name:   string(setup.CheckListenPort),
		result: setup.Fail(string(setup.CheckListenPort), wrapped, "addr in use"),
	}))

	report := r.Run(context.Background())
	require.ErrorIs(t, report.Results[0].Err, setup.ErrBindConflict)
	require.True(t, strings.Contains(report.Results[0].RemedyHint, "tailscale ip -4"))
}
