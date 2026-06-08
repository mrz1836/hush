package supervise_test

import (
	"context"
	"fmt"
	"net"
	"testing"

	"github.com/mrz1836/hush/internal/supervise"
)

// TestAllocateBackendPort_BindableLoopback asserts the allocated port can
// be reopened on 127.0.0.1 immediately after AllocateBackendPort returns.
// This is the contract Phase 5's lifecycle swap relies on: hand the port
// to the child, the child binds it. A port the kernel cannot hand back
// would silently route proxy traffic to a dead backend.
func TestAllocateBackendPort_BindableLoopback(t *testing.T) {
	port, err := supervise.AllocateBackendPort(context.Background())
	if err != nil {
		t.Fatalf("AllocateBackendPort: %v", err)
	}
	if port == 0 {
		t.Fatalf("AllocateBackendPort returned zero port")
	}
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	var lc net.ListenConfig
	l, err := lc.Listen(context.Background(), "tcp", addr)
	if err != nil {
		t.Fatalf("rebind %s: %v", addr, err)
	}
	defer func() { _ = l.Close() }()
	// Confirm the listener bound the exact requested port.
	got, ok := l.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("listener addr %T is not *net.TCPAddr", l.Addr())
	}
	if got.Port != int(port) {
		t.Fatalf("rebound on wrong port: want %d got %d", port, got.Port)
	}
	if got.IP.IsUnspecified() || !got.IP.IsLoopback() {
		t.Fatalf("rebound on non-loopback address: %s", got.IP)
	}
}

// TestAllocateBackendPort_Distinct asserts back-to-back allocations
// produce distinct ports under normal conditions. The kernel is not
// required to hand out a strictly increasing sequence, but two adjacent
// allocations should not collide because the first listener is still
// open while the second runs.
func TestAllocateBackendPort_Distinct(t *testing.T) {
	// Hold the first listener open across the second allocation so the
	// kernel cannot hand back the same port. We mirror what
	// AllocateBackendPort does internally — bind 127.0.0.1:0 — then call
	// AllocateBackendPort while the first listener is alive.
	var lc net.ListenConfig
	first, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("first Listen: %v", err)
	}
	defer func() { _ = first.Close() }()
	firstAddr, ok := first.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("first listener addr %T is not *net.TCPAddr", first.Addr())
	}

	second, err := supervise.AllocateBackendPort(context.Background())
	if err != nil {
		t.Fatalf("AllocateBackendPort: %v", err)
	}
	if int(second) == firstAddr.Port {
		t.Fatalf("AllocateBackendPort collided with held listener on port %d", second)
	}
}
