package server

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/mrz1836/hush/internal/config"
)

// startupCheck is a single named verification with an explicit Run function.
type startupCheck struct {
	name string
	run  func(context.Context) error
}

// startupChecks returns the locked-order check sequence:
// clock_sync → file_modes → tailscale_bind → state_dir.
func (s *Server) startupChecks() []startupCheck {
	return []startupCheck{
		{name: "clock_sync", run: s.checkClockSync},
		{name: "file_modes", run: s.checkFileModes},
		{name: "tailscale_bind", run: s.checkTailscaleBind},
		{name: "state_dir", run: s.checkStateDir},
	}
}

// runStartupChecks iterates the check sequence and short-circuits on the
// first non-nil error. Each error wraps a sentinel; callers match via
// errors.Is.
func (s *Server) runStartupChecks(ctx context.Context) error {
	for _, c := range s.startupChecks() {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := c.run(ctx); err != nil {
			return err
		}
	}
	return nil
}

// checkClockSync invokes the configured probe under a bounded timeout and
// returns ErrClockUnsynchronised when the host is unsynchronised, drifted
// beyond Cfg.Security.MaxClockDrift, or the probe fails.
//
// On success the result is cached on the Server (s.clockInSync.Store(true))
// so the /hz handler can report `clock_in_sync` without re-running the
// probe per request. When RequireNTPSync is false the cache is also
// flagged true (the operator opted out of the gate; reporting "in sync"
// matches the gate's verdict).
//
// When [Deps.AllowClockSkew] is set, a would-be failure is downgraded to
// a logged warning and a single [AuditClockSkewOverride] event; the
// chassis continues startup. [Deps.AllowClockProbeUnavailable] is the
// narrower smoke-only path: it downgrades all-provider timeout/cache-miss
// failures but never confirmed unsynchronised or drifted clocks. /hz still
// reports clock_in_sync=false so downstream tooling can see the truth.
func (s *Server) checkClockSync(ctx context.Context) error {
	if !s.cfg.Security.RequireNTPSync {
		s.clockInSync.Store(true)
		return nil
	}
	probeCtx, cancel := context.WithTimeout(ctx, DefaultClockSyncTimeout)
	defer cancel()

	synced, drift, err := s.clockProbe(probeCtx)
	switch {
	case err != nil:
		reason := "probe failed"
		if errors.Is(err, ErrClockProbeUnavailable) {
			reason = "probe unavailable"
		}
		return s.handleClockFailure(ctx,
			reason,
			fmt.Errorf("server: clock_sync: probe failed: %w: %w", ErrClockUnsynchronised, err))
	case !synced:
		return s.handleClockFailure(ctx,
			"not_synchronised",
			fmt.Errorf("server: clock_sync: host clock not NTP-synchronised: %w", ErrClockUnsynchronised))
	}
	if abs := absDuration(drift); abs > s.cfg.Security.MaxClockDrift {
		return s.handleClockFailure(ctx,
			fmt.Sprintf("drift %v exceeds %v", drift, s.cfg.Security.MaxClockDrift),
			fmt.Errorf("server: clock_sync: drift %v exceeds %v: %w",
				drift, s.cfg.Security.MaxClockDrift, ErrClockUnsynchronised))
	}
	s.clockInSync.Store(true)
	return nil
}

// handleClockFailure converts a clock-sync failure into either the
// historical hard-error return (default) or a logged warning + audit
// override (when --allow-clock-skew is set). Returning nil from the
// override branch lets the rest of the startup-check sequence run.
func (s *Server) handleClockFailure(ctx context.Context, reason string, err error) error {
	if !s.allowClockSkew {
		if s.allowClockProbeUnavailable && errors.Is(err, ErrClockProbeUnavailable) {
			s.logger.WarnContext(
				ctx, "clock check downgraded (network timeout)",
				"reason", reason,
				"err", err.Error(),
			)
			return nil
		}
		return err
	}
	if writeErr := s.audit.Write(ctx, AuditEvent{
		Type: AuditClockSkewOverride,
		At:   s.clock(),
		Detail: map[string]string{
			"reason": reason,
		},
	}); writeErr != nil {
		s.logger.WarnContext(ctx, "audit write clock_skew_override failed", "err", writeErr.Error())
	}
	s.logger.WarnContext(
		ctx, "clock-sync override active",
		"reason", reason,
		"err", err.Error(),
	)
	return nil
}

// absDuration returns the absolute value of d.
func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// checkFileModes walks the configured state directory and rejects any
// regular file with mode > 0600 or any directory with mode > 0700. The
// error names the offending path category but never reads file contents.
func (s *Server) checkFileModes(_ context.Context) error {
	if !s.cfg.Security.RequireFileModeChecks {
		return nil
	}
	stateDir := s.cfg.Server.StateDir

	if err := checkStateDirRoot(stateDir); err != nil {
		return err
	}

	walkErr := filepath.WalkDir(stateDir, walkModeCallback)
	if walkErr != nil {
		if errors.Is(walkErr, ErrFileModeLoose) {
			return walkErr
		}
		return fmt.Errorf("server: file_modes: walk %q: %w", stateDir, ErrFileModeLoose)
	}
	return nil
}

// checkStateDirRoot validates the state directory's own mode and type.
func checkStateDirRoot(stateDir string) error {
	rootInfo, err := os.Lstat(stateDir)
	if err != nil {
		return fmt.Errorf("server: file_modes: lstat %q: %w", stateDir, ErrFileModeLoose)
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("server: file_modes: %q is a symlink (root): %w", stateDir, ErrFileModeLoose)
	}
	if !rootInfo.IsDir() {
		return fmt.Errorf("server: file_modes: %q is not a directory: %w", stateDir, ErrFileModeLoose)
	}
	if mode := rootInfo.Mode().Perm(); mode > 0o700 {
		return fmt.Errorf("server: file_modes: directory %q mode %#o > 0700: %w",
			stateDir, mode, ErrFileModeLoose)
	}
	return nil
}

// walkModeCallback is the per-entry callback driven by filepath.WalkDir; it
// rejects regular files with mode > 0600 and directories with mode > 0700.
func walkModeCallback(path string, d fs.DirEntry, err error) error {
	if err != nil {
		return err
	}
	info, infoErr := d.Info()
	if infoErr != nil {
		return infoErr
	}
	mode := info.Mode().Perm()
	switch {
	case d.IsDir():
		if mode > 0o700 {
			return fmt.Errorf("server: file_modes: directory %q mode %#o > 0700: %w",
				path, mode, ErrFileModeLoose)
		}
	case info.Mode().IsRegular():
		if mode > 0o600 {
			return fmt.Errorf("server: file_modes: regular file %q mode %#o > 0600: %w",
				path, mode, ErrFileModeLoose)
		}
	}
	return nil
}

// checkTailscaleBind verifies that the configured listen address is inside
// the Tailscale CGNAT range (100.64.0.0/10) and bound to a local interface.
func (s *Server) checkTailscaleBind(_ context.Context) error {
	addr := s.cfg.Server.ListenAddr.Addr()
	if !addr.IsValid() {
		return fmt.Errorf("server: tailscale_bind: listen address is not valid: %w", ErrBindNotOnTailscale)
	}
	if !config.TailscaleCGNAT.Contains(addr) {
		return fmt.Errorf("server: tailscale_bind: %q not in %v: %w",
			addr, config.TailscaleCGNAT, ErrBindNotOnTailscale)
	}
	addrs, err := s.interfaceLister()
	if err != nil {
		return fmt.Errorf("server: tailscale_bind: enumerate interfaces: %w", ErrBindNotOnTailscale)
	}
	for _, a := range addrs {
		if matchesAddr(a, addr) {
			return nil
		}
	}
	return fmt.Errorf("server: tailscale_bind: %q not bound to any local interface: %w",
		addr, ErrBindNotOnTailscale)
}

// matchesAddr reports whether the local interface address a equals want.
// Accepts either *net.IPNet (most common return from net.InterfaceAddrs) or
// any net.Addr whose String() parses as an IP/CIDR.
func matchesAddr(a net.Addr, want netip.Addr) bool {
	if ipNet, ok := a.(*net.IPNet); ok {
		got, ok := netip.AddrFromSlice(ipNet.IP)
		if !ok {
			return false
		}
		return got.Unmap() == want.Unmap()
	}
	prefix, err := netip.ParsePrefix(a.String())
	if err == nil {
		return prefix.Addr().Unmap() == want.Unmap()
	}
	got, err := netip.ParseAddr(a.String())
	if err != nil {
		return false
	}
	return got.Unmap() == want.Unmap()
}

// checkStateDir verifies that the configured state directory exists, is a
// directory (not a symlink), and is owned by the running user.
func (s *Server) checkStateDir(_ context.Context) error {
	stateDir := s.cfg.Server.StateDir
	info, err := os.Lstat(stateDir)
	if err != nil {
		return fmt.Errorf("server: state_dir: lstat %q: %w", stateDir, ErrStateDirUnsafe)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("server: state_dir: %q is a symlink: %w", stateDir, ErrStateDirUnsafe)
	}
	if !info.IsDir() {
		return fmt.Errorf("server: state_dir: %q is not a directory: %w", stateDir, ErrStateDirUnsafe)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	if int(st.Uid) != os.Getuid() {
		return fmt.Errorf("server: state_dir: %q owned by uid %d, want %d: %w",
			stateDir, st.Uid, os.Getuid(), ErrStateDirUnsafe)
	}
	return nil
}

// failedCheckName extracts the check name from a wrapped startup-check
// sentinel error so callers can surface it in audit and operational logs.
func failedCheckName(err error) string {
	switch {
	case errors.Is(err, ErrClockUnsynchronised):
		return "clock_sync"
	case errors.Is(err, ErrFileModeLoose):
		return "file_modes"
	case errors.Is(err, ErrBindNotOnTailscale):
		return "tailscale_bind"
	case errors.Is(err, ErrStateDirUnsafe):
		return "state_dir"
	default:
		return "unknown"
	}
}
