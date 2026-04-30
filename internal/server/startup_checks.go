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
func (s *Server) checkClockSync(ctx context.Context) error {
	if !s.cfg.Security.RequireNTPSync {
		return nil
	}
	probeCtx, cancel := context.WithTimeout(ctx, DefaultClockSyncTimeout)
	defer cancel()

	synced, drift, err := s.clockProbe(probeCtx)
	if err != nil {
		return fmt.Errorf("server: clock_sync: probe failed: %w", ErrClockUnsynchronised)
	}
	if !synced {
		return fmt.Errorf("server: clock_sync: host clock not NTP-synchronised: %w", ErrClockUnsynchronised)
	}
	if abs := absDuration(drift); abs > s.cfg.Security.MaxClockDrift {
		return fmt.Errorf("server: clock_sync: drift %v exceeds %v: %w",
			drift, s.cfg.Security.MaxClockDrift, ErrClockUnsynchronised)
	}
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
