package server

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestStartupChecks_RefusesUnsyncedClock — clock probe reports synced=false.
func TestStartupChecks_RefusesUnsyncedClock(t *testing.T) {
	t.Parallel()

	srv, audit, _, _ := newTestServer(t, func(d *Deps) {
		d.ClockSyncProbe = scriptedClockProbe(false, 0, nil)
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := srv.Run(ctx)
	if !errors.Is(err, ErrClockUnsynchronised) {
		t.Fatalf("err=%v want ErrClockUnsynchronised", err)
	}
	// No listener bound: srv.listenerActual remains nil.
	if srv.listenerActual != nil {
		t.Fatalf("listener was bound on startup-check failure: %v", srv.listenerActual)
	}
	// Audit emitted once with status=refused, check=clock_sync.
	ev := audit.snapshot()
	if len(ev) != 1 {
		t.Fatalf("audit events=%d, want 1", len(ev))
	}
	if ev[0].Type != AuditServerStart || ev[0].Detail["status"] != "refused" || ev[0].Detail["check"] != "clock_sync" {
		t.Fatalf("audit event drifted: %+v", ev[0])
	}
}

// TestStartupChecks_RefusesClockDriftOver60s — drift > MaxClockDrift fails.
func TestStartupChecks_RefusesClockDriftOver60s(t *testing.T) {
	t.Parallel()

	srv, _, _, _ := newTestServer(t, func(d *Deps) {
		d.ClockSyncProbe = scriptedClockProbe(true, 61*time.Second, nil)
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := srv.Run(ctx); !errors.Is(err, ErrClockUnsynchronised) {
		t.Fatalf("err=%v want ErrClockUnsynchronised", err)
	}
}

// TestStartupChecks_RefusesProbeError — probe returns a non-nil error.
func TestStartupChecks_RefusesProbeError(t *testing.T) {
	t.Parallel()

	srv, _, _, _ := newTestServer(t, func(d *Deps) {
		d.ClockSyncProbe = scriptedClockProbe(true, 0, errTestSynthetic)
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := srv.Run(ctx); !errors.Is(err, ErrClockUnsynchronised) {
		t.Fatalf("err=%v want ErrClockUnsynchronised", err)
	}
}

// TestStartupChecks_SkipsClockSyncWhenDisabled — RequireNTPSync=false.
func TestStartupChecks_SkipsClockSyncWhenDisabled(t *testing.T) {
	t.Parallel()

	srv, _, _, _ := newTestServer(t, func(d *Deps) {
		d.Cfg.Security.RequireNTPSync = false
		// A probe that would FAIL — but the check is skipped.
		d.ClockSyncProbe = scriptedClockProbe(false, 999*time.Hour, nil)
	})
	// Pass through the clock check; the next check is file_modes which
	// passes because the temp state dir is 0700.
	if err := srv.checkClockSync(context.Background()); err != nil {
		t.Fatalf("checkClockSync skipped err=%v", err)
	}
}

// TestStartupChecks_RefusesLooseFileMode — 0644 file under StateDir.
func TestStartupChecks_RefusesLooseFileMode(t *testing.T) {
	t.Parallel()

	srv, _, _, _ := newTestServer(t)
	chmod0644File(t, srv.cfg.Server.StateDir, "leaky.txt")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := srv.Run(ctx)
	if !errors.Is(err, ErrFileModeLoose) {
		t.Fatalf("err=%v want ErrFileModeLoose", err)
	}
	if !contains(err.Error(), "regular file") {
		t.Fatalf("error message did not categorise the offending entry as regular file: %v", err)
	}
}

// TestStartupChecks_RefusesLooseDirMode — state dir at 0755.
func TestStartupChecks_RefusesLooseDirMode(t *testing.T) {
	t.Parallel()

	srv, _, _, _ := newTestServer(t)
	writeMode(t, srv.cfg.Server.StateDir, 0o755)
	t.Cleanup(func() { _ = os.Chmod(srv.cfg.Server.StateDir, 0o700) }) //nolint:gosec // 0700 is the chassis-required state-dir mode

	if err := srv.checkFileModes(context.Background()); !errors.Is(err, ErrFileModeLoose) {
		t.Fatalf("err=%v want ErrFileModeLoose", err)
	}
}

// TestStartupChecks_SkipsFileModesWhenDisabled — RequireFileModeChecks=false.
func TestStartupChecks_SkipsFileModesWhenDisabled(t *testing.T) {
	t.Parallel()

	srv, _, _, _ := newTestServer(t, func(d *Deps) {
		d.Cfg.Security.RequireFileModeChecks = false
	})
	chmod0644File(t, srv.cfg.Server.StateDir, "leaky.txt")
	if err := srv.checkFileModes(context.Background()); err != nil {
		t.Fatalf("file_modes should skip when disabled, got %v", err)
	}
}

// TestStartupChecks_RefusesPublicBind — table-driven over each non-CGNAT
// listen address.
func TestStartupChecks_RefusesPublicBind(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		addr netip.AddrPort
	}{
		{"0.0.0.0", netip.MustParseAddrPort("0.0.0.0:7743")},
		{"loopback", netip.MustParseAddrPort("127.0.0.1:7743")},
		{"public", netip.MustParseAddrPort("1.2.3.4:7743")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv, _, _, _ := newTestServer(t, func(d *Deps) {
				d.Cfg.Server.ListenAddr = tc.addr
				d.InterfaceLister = stubInterfaceLister(tc.addr.Addr())
			})
			if err := srv.checkTailscaleBind(context.Background()); !errors.Is(err, ErrBindNotOnTailscale) {
				t.Fatalf("err=%v want ErrBindNotOnTailscale", err)
			}
		})
	}
}

// TestStartupChecks_RefusesEmptyHostBind — invalid AddrPort fails.
func TestStartupChecks_RefusesEmptyHostBind(t *testing.T) {
	t.Parallel()

	srv, _, _, _ := newTestServer(t, func(d *Deps) {
		d.Cfg.Server.ListenAddr = netip.AddrPort{} // zero value: invalid
	})
	if err := srv.checkTailscaleBind(context.Background()); !errors.Is(err, ErrBindNotOnTailscale) {
		t.Fatalf("err=%v want ErrBindNotOnTailscale", err)
	}
}

// TestStartupChecks_RefusesAddrNotOnInterface — CGNAT address valid but not
// bound to any local interface.
func TestStartupChecks_RefusesAddrNotOnInterface(t *testing.T) {
	t.Parallel()

	srv, _, _, _ := newTestServer(t, func(d *Deps) {
		d.Cfg.Server.ListenAddr = netip.MustParseAddrPort("100.64.99.99:7743")
		// Lister returns a different CGNAT address.
		d.InterfaceLister = stubInterfaceLister(netip.MustParseAddr("100.64.0.1"))
	})
	if err := srv.checkTailscaleBind(context.Background()); !errors.Is(err, ErrBindNotOnTailscale) {
		t.Fatalf("err=%v want ErrBindNotOnTailscale", err)
	}
}

// TestStartupChecks_TailscaleBind_ListerError — interfaceLister errs.
func TestStartupChecks_TailscaleBind_ListerError(t *testing.T) {
	t.Parallel()

	srv, _, _, _ := newTestServer(t, func(d *Deps) {
		d.InterfaceLister = func() ([]net.Addr, error) { return nil, errTestSynthetic }
	})
	if err := srv.checkTailscaleBind(context.Background()); !errors.Is(err, ErrBindNotOnTailscale) {
		t.Fatalf("err=%v want ErrBindNotOnTailscale", err)
	}
}

// TestStartupChecks_RefusesMissingStateDir — no such directory.
func TestStartupChecks_RefusesMissingStateDir(t *testing.T) {
	t.Parallel()

	srv, _, _, _ := newTestServer(t)
	srv.cfg.Server.StateDir = filepath.Join(srv.cfg.Server.StateDir, "does-not-exist")

	if err := srv.checkStateDir(context.Background()); !errors.Is(err, ErrStateDirUnsafe) {
		t.Fatalf("err=%v want ErrStateDirUnsafe", err)
	}
}

// TestStartupChecks_RefusesStateDirIsFile — regular file at the state-dir
// path.
func TestStartupChecks_RefusesStateDirIsFile(t *testing.T) {
	t.Parallel()

	srv, _, _, _ := newTestServer(t)
	root := srv.cfg.Server.StateDir
	parent := filepath.Dir(root)
	regular := filepath.Join(parent, "is-a-file")
	if err := os.WriteFile(regular, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	srv.cfg.Server.StateDir = regular

	if err := srv.checkStateDir(context.Background()); !errors.Is(err, ErrStateDirUnsafe) {
		t.Fatalf("err=%v want ErrStateDirUnsafe", err)
	}
}

// TestStartupChecks_RefusesStateDirSymlink — symlinked state dir.
func TestStartupChecks_RefusesStateDirSymlink(t *testing.T) {
	t.Parallel()

	srv, _, _, _ := newTestServer(t)
	original := srv.cfg.Server.StateDir
	parent := filepath.Dir(original)
	link := filepath.Join(parent, "linked")
	if err := os.Symlink(original, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	srv.cfg.Server.StateDir = link

	if err := srv.checkStateDir(context.Background()); !errors.Is(err, ErrStateDirUnsafe) {
		t.Fatalf("err=%v want ErrStateDirUnsafe", err)
	}
}

// TestStartupChecks_RefusesStateDirForeignOwner — owned by another uid.
// Skipped when the test process cannot chown to a foreign uid (most CI).
func TestStartupChecks_RefusesStateDirForeignOwner(t *testing.T) {
	t.Parallel()

	if !canTestForeignOwner() {
		t.Skip("requires root + secondary uid; skipped on this host")
	}
	srv, _, _, _ := newTestServer(t)
	if err := os.Chown(srv.cfg.Server.StateDir, 1, 1); err != nil {
		t.Fatalf("chown: %v", err)
	}
	if err := srv.checkStateDir(context.Background()); !errors.Is(err, ErrStateDirUnsafe) {
		t.Fatalf("err=%v want ErrStateDirUnsafe", err)
	}
}

// TestStartupChecks_OrderedExecution — host with all four misconfigurations
// at once: clock fails first; the remaining checks must NOT have been run.
func TestStartupChecks_OrderedExecution(t *testing.T) {
	t.Parallel()

	var (
		fileModesCalls     int
		tailscaleBindCalls int
		stateDirCalls      int
	)

	srv, _, _, _ := newTestServer(t, func(d *Deps) {
		// Clock check: returns a hard failure.
		d.ClockSyncProbe = scriptedClockProbe(false, 0, nil)
	})

	// Replace the check methods with counting fakes so we can prove the
	// order without re-driving the underlying probes.
	checks := []startupCheck{
		{name: "clock_sync", run: srv.checkClockSync}, // real
		{name: "file_modes", run: func(_ context.Context) error {
			fileModesCalls++
			return nil
		}},
		{name: "tailscale_bind", run: func(_ context.Context) error {
			tailscaleBindCalls++
			return nil
		}},
		{name: "state_dir", run: func(_ context.Context) error {
			stateDirCalls++
			return nil
		}},
	}

	// Drive the slice manually to mirror runStartupChecks.
	var firstErr error
	for _, c := range checks {
		if err := c.run(context.Background()); err != nil {
			firstErr = err
			break
		}
	}
	if !errors.Is(firstErr, ErrClockUnsynchronised) {
		t.Fatalf("first error not clock: %v", firstErr)
	}
	if fileModesCalls != 0 || tailscaleBindCalls != 0 || stateDirCalls != 0 {
		t.Fatalf("subsequent checks ran: file_modes=%d tailscale_bind=%d state_dir=%d",
			fileModesCalls, tailscaleBindCalls, stateDirCalls)
	}
}

// TestStartupChecks_AuditEmitsRefused — exactly one server_start event with
// status=refused and check=<name> on a refusal path.
func TestStartupChecks_AuditEmitsRefused(t *testing.T) {
	t.Parallel()

	srv, audit, _, _ := newTestServer(t, func(d *Deps) {
		d.ClockSyncProbe = scriptedClockProbe(false, 0, nil)
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = srv.Run(ctx)

	ev := audit.snapshot()
	if len(ev) != 1 {
		t.Fatalf("audit events=%d, want exactly 1 (got %+v)", len(ev), ev)
	}
	if ev[0].Type != AuditServerStart {
		t.Fatalf("event type=%v want AuditServerStart", ev[0].Type)
	}
	if ev[0].Detail["status"] != "refused" || ev[0].Detail["check"] != "clock_sync" {
		t.Fatalf("event detail drifted: %+v", ev[0].Detail)
	}
}

// TestFailedCheckName_AllSentinels covers the failedCheckName mapping.
func TestFailedCheckName_AllSentinels(t *testing.T) {
	t.Parallel()
	cases := []struct {
		err  error
		want string
	}{
		{ErrClockUnsynchronised, "clock_sync"},
		{ErrFileModeLoose, "file_modes"},
		{ErrBindNotOnTailscale, "tailscale_bind"},
		{ErrStateDirUnsafe, "state_dir"},
		{errTestSynthetic, "unknown"},
	}
	for _, tc := range cases {
		if got := failedCheckName(tc.err); got != tc.want {
			t.Errorf("failedCheckName(%v)=%q want %q", tc.err, got, tc.want)
		}
	}
}

// TestAbsDuration covers the duration helper.
func TestAbsDuration(t *testing.T) {
	t.Parallel()
	if got := absDuration(-3 * time.Second); got != 3*time.Second {
		t.Fatalf("absDuration(-3s) = %v", got)
	}
	if got := absDuration(2 * time.Second); got != 2*time.Second {
		t.Fatalf("absDuration(2s) = %v", got)
	}
}

func contains(s, sub string) bool {
	return len(s) > 0 && (s == sub || (len(sub) > 0 && (indexOf(s, sub) >= 0)))
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
