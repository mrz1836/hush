// Package server is the HTTP chassis on which SDD-12 (claim) and SDD-13
// (secret/revoke/health) attach handlers and SDD-14 (`cmd/hush serve`) runs
// as the lifecycle owner. The chassis owns four observable properties:
//
//  1. Refuse-to-start on a misconfigured host. Four ordered startup checks —
//     clock_sync → file_modes → tailscale_bind → state_dir — run before any
//     socket is bound. Each failure returns a typed sentinel and exits non-zero.
//  2. Atomic SIGHUP vault reload with a drain window. Reload loads the new
//     vault, atomically swaps `*atomic.Pointer[vault.Store]`, and destroys the
//     previous store after the configured drain window. Reloads are serialised.
//  3. Middleware chain that never leaks request bodies. Order is request ID →
//     IP allow-list → body cap → panic recover → handler. Recover logs the
//     panic value, the stack, and the request_id at ERROR; the request body is
//     never part of the log entry. Allow-list rejects with 403 before any
//     handler runs. The chassis assigns the request ID itself; client-supplied
//     headers are ignored.
//  4. Graceful shutdown. `Run(ctx)` blocks until the context cancels, then
//     triggers `http.Server.Shutdown` with `ShutdownTimeout` (default 30 s)
//     and waits for any pending reload's drain so vault memory is not leaked
//     across the process exit.
//
// The package also declares the Approver interface (with ApprovalRequest /
// Decision value types) — SDD-11's Discord-backed implementation will satisfy
// it without modifying the chassis. AuditWriter is the consumer-side
// interface the chassis uses to emit security-relevant events; SDD-13 supplies
// the concrete implementation.
//
// Constitutional principles in scope:
//   - III (defense in depth): the chassis is the mounting surface for every
//     downstream layer (signature verify, IP allow-list, JWT validate, ECIES).
//   - VI (Tailscale-only bind): the tailscale_bind startup check refuses any
//     listen address that is not in Tailscale CGNAT (100.64.0.0/10).
//   - VIII (testing discipline): coverage target 95 %; race-detector run
//     mandatory; every behaviour contract is pinned by a test.
//   - IX (idiomatic Go): no init(), no mutable package-level globals beyond
//     sentinel errors; context.Context never stored in a struct field.
//   - X (observability & redaction): recover middleware emits panic + stack
//   - request_id and never any byte of the request body.
//   - XI (native-first): `net/http.ServeMux` is the only router; zero new
//     third-party dependencies.
//
// Exported entry points:
//
//   - [New] — constructs a chassis from Deps; performs no I/O.
//   - [Server.Run] — runs the lifecycle: startup checks → bind → serve →
//     shutdown.
//   - [Server.ReloadVault] — loads a new vault and atomically swaps it.
//   - [Server.Mount] — registers a (method, path, handler) tuple under the
//     mounted prefix; must be called before [Server.Run] starts the listener.
//   - [RequestID] — accessor for the chassis-assigned request ID.
//   - [Approver], [ApprovalRequest], [Decision], [SessionType] — approval
//     interface and value types consumed by SDD-12.
//   - [AuditWriter], [AuditEvent], [AuditEventType] — audit emission interface.
package server
