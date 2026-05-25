// Backend port allocation for reload-eligible supervisors (T-306 Phase 4).
//
// backend_port.go owns the loopback-only port allocation used when
// [child.handoff] mode = "http-proxy". The supervisor binds the public
// listener (config.Child.Handoff.ListenAddr) and forwards traffic to a
// private 127.0.0.1:<port> backend the child opens. The allocator hands
// back a port that was just bound and immediately released, so the child
// can rebind it on Start. The OS may reassign the port between this
// function returning and the child's Bind; that race is acceptable
// because the readiness prober (Phase 3) is the source of truth for
// whether the new backend ever became serving — a Bind failure surfaces
// as a readiness timeout and the swap orchestrator rolls back.
//
// This file is independent of Lifecycle so unit tests can exercise it
// without standing up a full supervisor.

package supervise

import (
	"context"
	"errors"
	"fmt"
	"net"
)

// errBackendPortAllocate backs the sentinel returned for any allocation
// failure. Programmer- or environment-error class; not part of any
// operator-visible contract.
var errBackendPortAllocate = errors.New("supervise: backend port allocate")

// AllocateBackendPort asks the kernel for an ephemeral TCP port on
// 127.0.0.1 and returns it. The returned port is guaranteed to be
// loopback-only — backend traffic never traverses the public listener
// or any non-loopback interface. The intermediate listener is closed
// before AllocateBackendPort returns so the child can immediately
// rebind it.
//
// The ctx is propagated to the kernel listen call so a cancelled
// supervisor context aborts allocation rather than blocking on a
// pathological setsockopt path. A non-nil error is returned only
// when the kernel itself cannot satisfy the request (ctx cancelled,
// file descriptor exhaustion, ports exhausted) — callers should
// surface this as a swap-precondition failure rather than crashing
// the supervisor.
func AllocateBackendPort(ctx context.Context) (uint16, error) {
	var lc net.ListenConfig
	l, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("%w: %w", errBackendPortAllocate, err)
	}
	defer func() { _ = l.Close() }()
	addr, ok := l.Addr().(*net.TCPAddr)
	if !ok {
		return 0, fmt.Errorf("%w: unexpected listener addr type %T", errBackendPortAllocate, l.Addr())
	}
	if addr.Port <= 0 || addr.Port > 0xFFFF {
		return 0, fmt.Errorf("%w: port %d out of range", errBackendPortAllocate, addr.Port)
	}
	return uint16(addr.Port), nil
}
