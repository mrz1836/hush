# Contract — `internal/vault/securebytes` exported API (SDD-02)

This file is the locked Go API for `internal/vault/securebytes`. It
will be copied verbatim into `docs/PACKAGE-MAP.md` under "##
`internal/vault/` → ### Exported API — locked at SDD-02
(`securebytes` subpackage)" once SDD-02 enters the implement phase.
**No symbol may be added, removed, or renamed without an SDD
amendment.**

Path: `github.com/mrz1836/hush/internal/vault/securebytes`

```go
// Package securebytes provides the SecureBytes container — an opaque,
// pointer-only secret holder that pins its payload in non-swappable
// memory, zeroes the payload on explicit Destroy AND on garbage
// collection (via a runtime finalizer), and renders as the literal
// string "[redacted]" through every standard log/format/JSON path.
//
// SecureBytes is the foundation of hush's Layer 5 (mlocked secure
// memory) defence; see docs/SECURITY.md §3.5 and §6.
//
// Known residual risk: the Go runtime may transiently copy heap
// objects during GC compaction. mlock pins the current backing
// region against swap and against relocation of the pinned region,
// but cannot prevent a transient copy in pathological cases. This
// is documented as outside the package's threat model (commodity
// malware enumerating dotfiles, NOT root-level memory forensics)
// and no bandaid mitigation is added.
package securebytes

import (
    "errors"
    "log/slog"
)

// SecureBytes wraps a binary payload under three simultaneous
// protections: memory pinning (mlock), type-driven render
// redaction, and zero-on-destroy. The zero value is NOT a valid
// container — instances must be constructed via New and used
// through the returned pointer.
type SecureBytes struct { /* unexported fields */ }

// New constructs a SecureBytes wrapping a copy of b.
//
// The constructor allocates a fresh buffer, copies b into it,
// pins the new buffer in non-swappable memory (mlock), then
// zeroes b. After New returns, b contains only zero bytes and
// the only live copy of the original payload is held inside
// the returned container.
//
// Any length is permitted, including 0 (a zero-length container
// is a valid degenerate container).
//
// If the host operating system refuses the swap-protection
// request (e.g. RLIMIT_MEMLOCK is exhausted), New returns
// nil and a wrapped errno error. Callers may unwrap with
// errors.Is(err, syscall.EAGAIN) etc.
//
// New also registers a runtime finalizer that calls Destroy
// if the returned reference becomes unreachable without an
// explicit Destroy.
func New(b []byte) (*SecureBytes, error)

// Use invokes fn with the container's payload buffer. The buffer
// is the container's own mlocked storage, NOT a copy. The
// callback MUST NOT retain the slice past the call — doing so
// is a caller bug that defeats the package's lifetime guarantees.
//
// Use serialises with Destroy: while Use is running, a concurrent
// Destroy will block until the callback returns. While the
// callback is running, no other Use call from another goroutine
// will execute concurrently against the same container.
//
// Use returns ErrDestroyed if the container has already been
// destroyed (whether explicitly or by finalizer). In that case
// fn is NOT invoked.
//
// A panic from fn does NOT corrupt the container — the mutex
// is released and the container remains live for subsequent
// callers (they may borrow, destroy, or render it).
func (sb *SecureBytes) Use(fn func(b []byte)) error

// Len reports the byte length of the payload.
//
// While the container is live, Len returns the length passed to
// New. After Destroy (or finalizer), Len returns 0.
func (sb *SecureBytes) Len() int

// Destroy zeroes the payload buffer and releases the
// swap-protection. After Destroy, Use returns ErrDestroyed and
// Len reports 0.
//
// Destroy is idempotent — calling it on an already-destroyed
// container is a no-op and returns nil.
//
// In rare cases, the underlying munlock syscall may report an
// error; Destroy returns it wrapped via fmt.Errorf("...: %w", err).
// Callers may unwrap as for New.
func (sb *SecureBytes) Destroy() error

// LogValue implements slog.LogValuer.
//
// Always returns slog.StringValue("[redacted]"). Does not consult
// the container's lifecycle state — a destroyed container renders
// identically to a live one.
func (sb *SecureBytes) LogValue() slog.Value

// String implements fmt.Stringer.
//
// Always returns "[redacted]".
func (sb *SecureBytes) String() string

// MarshalJSON implements json.Marshaler.
//
// Always returns the JSON-encoded literal string "[redacted]"
// (i.e. []byte(`"[redacted]"`)) and a nil error.
func (sb *SecureBytes) MarshalJSON() ([]byte, error)

// ErrDestroyed is returned by Use when invoked on a container
// whose payload has already been zeroed (whether by explicit
// Destroy or by the runtime finalizer).
//
// Callers compare via errors.Is(err, securebytes.ErrDestroyed).
//
// The error message identifies the failure mode and the package
// path only; it carries no payload bytes, no length, and no
// other diagnostic.
var ErrDestroyed = errors.New("hush/vault/securebytes: destroyed")
```

## Behavioural guarantees (test-enforced)

| # | Guarantee | Test |
|---|-----------|------|
| G1 | `New` copies the input into a fresh buffer and zeroes the caller's input slice before returning. | `TestSecureBytes_New_CopiesAndZeroesInput` |
| G2 | `Use` invokes its callback with the exact original bytes; concurrent borrows are safe; a panic in the callback leaves the container live. | `TestSecureBytes_Use_DeliversPayload` (with sub-tests for concurrent and panicking callbacks) |
| G3 | After `Destroy`, the previously-held buffer contains only zero bytes; second `Destroy` is a no-op (idempotent); `Destroy` returns `nil` on success. | `TestSecureBytes_Destroy_ZeroesAndIdempotent` |
| G4 | After `Destroy`, `Use` returns `ErrDestroyed` (callback NOT invoked); `Len` reports 0. | `TestSecureBytes_PostDestroy_ReturnsErrDestroyed` |
| G5 | `LogValue`, `String`, and `MarshalJSON` all return the literal `"[redacted]"` for both LIVE and DESTROYED containers. | `TestSecureBytes_Render_RedactsAllPaths` |
| G6 | Sentinel-leak: a SecureBytes wrapping `SECRET_SHOULD_NEVER_APPEAR_2` rendered through a `slog.JSONHandler` writing into a buffer, through `fmt.Sprintf("%s"/"%v", sb)`, and through `json.Marshal(sb)` produces output containing `"[redacted]"` and zero occurrences of the sentinel. | `TestSecureBytes_RedactionSentinel` |
| G7 | A `*SecureBytes` that becomes unreachable without explicit Destroy has its finalizer trigger Destroy before the underlying memory is recycled. | `TestSecureBytes_FinalizerZerosOnGC` (forces `runtime.GC()` twice; asserts a side-channel flag was set by a test-only finalizer wrapper) |
| G8 | `go test -race ./internal/vault/securebytes/` clean under N concurrent `Use` callers against the same live container. | `TestSecureBytes_ConcurrentUse` |
| G9 | 100% line coverage. | `go test -cover ./internal/vault/securebytes/` reports 100.0%. |
| G10 | The package builds and tests pass on darwin AND linux under `CGO_ENABLED=0`. | CI matrix (already configured); local verification via `magex test:race` on each platform. |

## Negative-space contract (test-enforced)

The package MUST NOT:

- Expose a public accessor that returns the payload bytes (no
  `Bytes()`, no `Get()`, no `Slice()`). **Verification:** `grep -nE
  'func \(sb \*SecureBytes\) (Bytes|Get|Slice|Copy)' internal/vault/securebytes/*.go`
  returns nothing.
- Accept `string` as the constructor input type. **Verification:**
  `grep -nE 'func New[A-Za-z]*\([^)]*string' internal/vault/securebytes/*.go`
  returns nothing.
- Use cgo. **Verification:** `grep -nE '^import "C"|/\*\s*#include'
  internal/vault/securebytes/*.go` returns nothing; the file
  contains no `import "C"` declaration; CI runs with
  `CGO_ENABLED=0`.
- Use `unsafe` outside the syscall wrappers (and there are no
  syscall wrappers using `unsafe` because `golang.org/x/sys/unix`
  encapsulates that). **Verification:** `grep -n '"unsafe"'
  internal/vault/securebytes/*.go` returns nothing.
- Have any `init()` function (Constitution IX). **Verification:**
  `grep -nE '^func init\(\)' internal/vault/securebytes/*.go`
  returns nothing.
- Have any package-level mutable state. **Verification:**
  `gochecknoglobals` lint (already enabled in `.golangci.json`).
- Convert any secret-bearing `[]byte` to `string`. **Verification:**
  `grep -nE 'string\(sb\.buf|string\(buf|string\(b\)' internal/vault/securebytes/*.go`
  returns nothing. (The package does emit string-typed values, but
  only the literal `"[redacted]"` and the static error message —
  neither is secret.)
- Import any path under `github.com/mrz1836/hush/internal/...`
  (leaf-package rule, same as `internal/keys`). **Verification:**
  `go list -deps ./internal/vault/securebytes/` lists only stdlib
  + `golang.org/x/sys/unix`.
- Log anything (no logger import). **Verification:** `grep -nE
  'log/slog\."?[A-Z]' internal/vault/securebytes/*.go` shows only
  the `slog.Value` / `slog.StringValue` / `slog.LogValuer` symbols
  used by the `LogValue` method — no calls to `slog.Info`,
  `slog.Default`, etc.

## Mandatory fuzz target

**None.** Cross-checked against `docs/TESTING-STRATEGY.md` §2 and
the constitution's "Mandatory fuzz targets" list (vault file decode,
JWT parse, ECIES decrypt, request signature payload, supervisor
TOML, status socket JSON encoding) — `securebytes` has no parser
surface and is not on either list. Downstream packages (vault file
in SDD-03; JWT in SDD-07; etc.) carry the fuzz targets that exercise
the parser surfaces; this package's payload is opaque bytes.

## Constants exposed (unexported, but documented for review)

These are not part of the public API but are pinned by the chunk
contract; they MUST be encoded as named package identifiers
(constants or single-purpose helpers) so a reviewer can grep them
in one place.

```go
// Render literal — must match across every render method.
const redactedLiteral = "[redacted]"

// JSON-encoded form of the render literal — pre-encoded so
// MarshalJSON never has to read the buffer.
var redactedJSON = []byte(`"[redacted]"`)
```

The exact representation (string constant, `[]byte` constant via
`var`, or pre-computed at package init via a `func init()` —
forbidden) is an implementation detail; what matters is that the
literal is named, grep-able, and documented in this contract.
