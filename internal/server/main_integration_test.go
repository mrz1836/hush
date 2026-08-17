//go:build integration

package server

import (
	"os"
	"os/signal"
	"syscall"
	"testing"
)

// TestMain installs a process-lifetime SIGHUP sink for the integration build.
//
// Several integration tests raise SIGHUP at their own PID to exercise the
// server's reload path (TestSIGHUP_AtomicReload) and shutdown-drop path
// (TestRun_GracefulShutdown_DrainsInflight). Each has a race window where the
// server's own signal.Notify registration is not yet installed (startup) or has
// already been removed via signal.Stop (shutdown). If SIGHUP is delivered inside
// that window with no channel registered, its default disposition terminates the
// entire test binary — surfacing as "signal: hangup" against whatever unrelated
// parallel test happened to be running, and failing the whole internal/server
// package. That flake only reproduces under load (it slipped through on darwin
// but killed the Linux CI runner).
//
// Keeping one never-Stopped registration alive for the binary's lifetime
// guarantees at least one channel is always registered, so the default terminate
// action can never fire. signal.Notify fans out to every registered channel, so
// the server still observes SIGHUP exactly as before; this sink only drains the
// extra copy and never triggers a reload of its own.
func TestMain(m *testing.M) {
	sink := make(chan os.Signal, 1)
	signal.Notify(sink, syscall.SIGHUP)
	// Intentionally never signal.Stop(sink): the guard must outlive every test.
	go func() {
		for range sink {
		}
	}()
	os.Exit(m.Run())
}
