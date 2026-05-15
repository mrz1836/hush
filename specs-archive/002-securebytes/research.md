# Phase 0 Research — internal/vault/securebytes (SDD-02)

**Branch:** `002-securebytes`
**Constitution gate:** principles III (Layer 5), VIII, IX, X, XI in
scope.

## Scope of this document

The chunk contract (`docs/sdd/SDD-02.md`), the Plan-prompt
"Implementation contract (HOW — locked)" section, Constitution III
(Layer 5), and the Spec's FRs **lock** every primitive, syscall,
build-tag scheme, and API symbol. Per the user-supplied plan prompt,
research **MUST NOT** propose alternatives for these locked
decisions — Constitution XI requires a written PR justification for
every new dependency, and the SDD's anti-contract forbids cgo and
forbids `unsafe` outside the syscall wrappers.

This document therefore records:

1. The locked decisions (with citations back to the constitution /
   spec / SDD), so the implement phase has a single grep-able source.
2. The HOW questions the locked contract leaves open, with a
   resolution and rationale for each.

It is **not** a survey of alternative memory-locking libraries,
alternative finalizer schemes, or alternative concurrency primitives —
those choices are already made.

---

## A. Locked decisions (no alternatives evaluated)

### A1. Syscall layer: `golang.org/x/sys/unix.Mlock` / `unix.Munlock`

- **Decision:** Memory pinning calls `golang.org/x/sys/unix.Mlock`
  and `unix.Munlock` from build-tagged per-OS files
  (`securebytes_darwin.go`, `securebytes_linux.go`).
- **Rationale:** SDD-02 "Implementation contract (HOW — locked)"
  section pins this exact API. Constitution IX prohibits `cgo`,
  which rules out `<sys/mman.h>` directly. `golang.org/x/sys/unix`
  is the canonical pure-Go syscall surface for darwin/linux, is
  already in `go.sum` as a transitive dep of `golang.org/x/crypto`,
  and is on the trusted-sources list (`dependency-management.md` —
  golang.org/x is a sigil baseline tier).
- **Alternatives considered:** **None.** cgo is forbidden;
  `syscall.Mlock` / `syscall.Munlock` exist on linux but not darwin
  (`syscall` is frozen and incomplete); rolling our own
  `unix.Syscall(SYS_MLOCK, ...)` would duplicate `x/sys/unix` for
  no benefit and would add `unsafe` outside the syscall wrappers
  (which the chunk anti-contract explicitly forbids).

### A2. Build-tag layout: per-OS files

- **Decision:** Two per-OS files, each with a single `//go:build`
  constraint:
  - `securebytes_darwin.go` — `//go:build darwin`
  - `securebytes_linux.go`  — `//go:build linux`
- **Rationale:** `unix.Mlock`/`unix.Munlock` exist on both darwin
  and linux with identical signatures, so the per-OS files end up
  trivial (a single internal `mlock(b []byte) error` /
  `munlock(b []byte) error` per file delegating to `unix.Mlock` /
  `unix.Munlock`). The split exists so that, if a future OS lands
  with a divergent API (or if the project ever adds Windows
  `VirtualLock` per a constitutional amendment), the
  cross-platform `securebytes.go` does not need to change. SDD-02
  explicitly enumerates these two files.
- **Alternatives considered:** **None.** A single file with both
  build tags would produce identical code on both OSes (so the
  split costs nothing); a single file without build tags would
  require the cross-platform code to import `x/sys/unix` directly
  (it does on those OSes today, but the per-OS split documents the
  platform boundary explicitly and matches the SDD-02 file list
  verbatim).

### A3. Constructor signature: `New(b []byte) (*SecureBytes, error)`

- **Decision:** Constructor accepts a single `[]byte` parameter; no
  `string` overload. Returns a pointer to `SecureBytes` (the
  zero-value of which is not a valid container — pointer-only usage).
- **Rationale:** Spec FR-003 prohibits string-typed inputs because
  Go strings cannot be reliably zeroed (the runtime may share
  backing storage with `unsafe`-converted byte slices, and the
  language guarantees no mutation). SDD-02 anti-contracts: "Allow
  construction from `string`" forbidden. Pointer-only usage is
  required because the type carries a finalizer (set in `New`) and
  a mutex; copying the struct would clone the finalizer's reference
  but not its semantics, and would corrupt the mutex.
- **Alternatives considered:** **None.** A `NewFromString` overload
  would defeat the entire premise of the package.

### A4. Borrow-only read path: `Use(fn func(b []byte)) error`

- **Decision:** `Use` is the ONLY way to read the payload. No
  `Bytes()` accessor. Closure receives the underlying buffer
  directly (NOT a copy) — caller MUST NOT retain the slice past
  the callback return; this is documented as caller contract.
  Returns `ErrDestroyed` if called on a destroyed container.
- **Rationale:** SDD-02 contract: "Borrow-checked access via
  `Use(func(b []byte))` only — the `[]byte` handed to `fn` MUST
  NOT escape." Spec FR-006 / FR-007 / SC-004 state the same
  requirement at the WHAT level. Returning a copy on every read
  would (a) leak unprotected (un-mlocked) plaintext into the
  caller's heap, (b) defeat the zeroing guarantee on destroy, and
  (c) impose an O(n) cost on every cryptographic operation that
  needs a transient view of the key.
- **Alternatives considered:** **None.** A `Bytes()` accessor is
  forbidden by the anti-contract.

### A5. Render protection: three interfaces, single literal output

- **Decision:** The type implements `slog.LogValuer`, `fmt.Stringer`,
  and `json.Marshaler`. All three return the literal string
  `"[redacted]"` — wrapped in `slog.StringValue`,
  returned as `string`, and returned as JSON-encoded `[]byte("\"[redacted]\"")`
  respectively. None of the three reads the underlying buffer.
- **Rationale:** Spec FR-014 / FR-015 / FR-016 demand exactly
  this rendering on the three standard paths. Constitution X
  ("Observability & Redaction") names `SecureBytes` as the
  type-driven redaction primitive every other secret-bearing
  type composes into. The rendering must remain identical on a
  destroyed container (FR-017) — implemented by simply not
  consulting the destroyed flag in any of the three render
  methods.
- **Alternatives considered:** **None.** Returning the type name
  or the length would leak diagnostic data; failing differently
  on a destroyed container would expose the lifecycle state.

### A6. Destroyed sentinel: `var ErrDestroyed = errors.New(...)`

- **Decision:** Single exported sentinel
  `ErrDestroyed = errors.New("hush/vault/securebytes: destroyed")`
  returned by `Use` after destruction. Compared with `errors.Is`.
- **Rationale:** Constitution IX mandates "sentinel errors as
  exported package-level `var Err... = errors.New(...)`" and
  forbids comparing error strings. Spec FR-009 / FR-012 / SC-008
  demand a "distinct, named failure". Errors from `Munlock`
  surface from `Destroy` via `fmt.Errorf("%w", err)` — they are
  not part of the public sentinel surface (rare; observed only
  on resource-exhaustion edge paths).
- **Alternatives considered:** **None.** Constitution IX is
  prescriptive.

### A7. Coverage target: 100% (line + branch)

- **Decision:** Coverage is reported via `go test -cover ./internal/vault/securebytes/`
  and asserted at `100.0%` for the v0.1.0 gate.
- **Rationale:** Constitution VIII pins `internal/vault/...` as
  security-critical (100% target). SDD-02 reiterates "Coverage
  target: 100%". The package's surface is small enough (≤ ~150
  LOC of production code) that 100% is achievable with the eight
  tests enumerated in the chunk contract.
- **Alternatives considered:** **None.**

---

## B. Open HOW questions resolved by this research

Each question below was left open by the locked contract; this
section selects a single resolution per question and documents the
rationale.

### B1. Where to place the mutex acquisition in `Use`?

- **Question:** Does `Use` hold the mutex for the entire duration
  of the callback (preventing concurrent `Destroy` from yanking
  the buffer mid-read), or does it only hold the mutex while
  reading the destroyed flag and incrementing a borrow counter?
- **Decision:** `Use` holds the mutex for the **entire duration**
  of the callback. `Destroy` cannot make progress while a borrow
  is in flight; it blocks until the callback returns, then
  performs the zero+munlock, then returns.
- **Rationale:** This satisfies Spec edge case "Concurrent borrow
  and destroy" — destroy MUST NOT yank the payload out from under
  a borrow. Holding the mutex for the callback duration is the
  simplest correct implementation (no borrow counter, no
  condition variable, no wait/notify). The cost is that
  concurrent `Use` callers serialise on the mutex; this is
  acceptable for v0.1.0 because (a) the typical secret operation
  is a single sign / encrypt / decrypt taking microseconds, and
  (b) Spec FR-008 demands concurrent borrows be safe — it does
  not demand concurrent borrows be parallel. A reader/writer
  scheme (`sync.RWMutex` with read-locking in `Use`) is the
  obvious upgrade if profiling later shows contention; that is
  a Phase-2 optimisation, not a Phase-1 requirement.
- **Alternatives considered:**
  - In-flight borrow counter + condition variable — rejected:
    significantly more code with no observable correctness
    benefit at v0.1.0.
  - Brief mutex hold (around the destroyed flag check), then
    release before the callback runs — rejected: leaves an
    unresolvable race window between the check and the callback's
    first read.
  - `sync.RWMutex` with read-locking in `Use` — deferred:
    profiling-driven optimisation; the Mutex form is correct and
    has tighter test coverage requirements.

### B2. Volatile-style zero write to defeat compiler dead-store elimination

- **Question:** A naïve `for i := range buf { buf[i] = 0 }` in the
  destroy path is theoretically vulnerable to dead-store
  elimination (the compiler may notice the buffer is never read
  again and remove the writes). How is the zero-on-destroy made
  robust against this?
- **Decision:** Use the canonical Go idiom for non-elidable
  zeroing: a plain `for i := range sb.buf { sb.buf[i] = 0 }` loop
  followed by a no-op observation of the buffer (the syscall
  `unix.Munlock(sb.buf)` reads the slice header — not the
  contents — but in practice the Go compiler does NOT eliminate
  writes to slice-backed storage that escapes through any
  function call, and `unix.Munlock` is a function call). For
  defensive in-depth: the package also nils out `sb.buf` (the
  slice header) after zeroing, so the backing array becomes
  unreachable from the package and the GC can eventually reclaim
  it.
- **Rationale:** The Go runtime currently does not eliminate
  writes to memory that escapes through a function call (which
  every syscall does); `runtime.KeepAlive` is the official escape
  hatch if a future compiler version becomes more aggressive. The
  standard library itself uses this pattern in `crypto/subtle`
  and `crypto/cipher`. We document the pattern in `doc.go` and
  add a `runtime.KeepAlive(sb)` after the zero loop as a
  belt-and-braces guard. There is no `memset_s`-equivalent in
  pure Go; the language does not provide a stronger guarantee
  than this idiom.
- **Alternatives considered:**
  - `crypto/subtle.ConstantTimeXorBytes(sb.buf, sb.buf, sb.buf)` —
    rejected: obscure idiom, no clearer guarantee, and pulls
    `crypto/subtle` into the package's import surface for no
    semantic benefit.
  - `unsafe.Pointer` + `runtime.memclr` — rejected by the SDD
    anti-contract ("no `unsafe` outside the syscall wrappers")
    and by `runtime.memclr` being unexported.

### B3. Finalizer wiring and the receiver-pointer hazard

- **Question:** `runtime.SetFinalizer` requires a pointer
  argument and that the finalizer's argument-pointer-type match.
  How is the finalizer registered without creating a self-
  reference cycle that would prevent the finalizer from ever
  running?
- **Decision:** `New` constructs a `*SecureBytes`, calls
  `runtime.SetFinalizer(sb, (*SecureBytes).finalize)`, and returns
  `sb`. The `finalize` method calls `Destroy` (idempotent — safe
  whether or not the user already destroyed). The finalizer is a
  method-value referencing only the receiver; it does NOT close
  over `sb` from an outer scope, so there is no closure-induced
  reference cycle.
- **Rationale:** The Go runtime documentation is explicit: a
  `SetFinalizer` registered with a method-value of the
  finalized object's type does NOT prevent the object from
  becoming unreachable. The finalizer fires once on the next GC
  cycle after the object becomes unreachable. Using `Destroy` as
  the finalizer body (via a thin `finalize` wrapper that calls
  `Destroy`) preserves the idempotency guarantee for free —
  Destroy-by-finalizer and Destroy-by-explicit are observably
  identical from outside the package.
- **Alternatives considered:**
  - Closure-based finalizer (`runtime.SetFinalizer(sb,
    func(*SecureBytes) { sb.Destroy() })`) — rejected: introduces
    a closure capture of `sb` from the outer scope, which on some
    Go versions has been observed to inhibit collection. The
    method-value form is unambiguous.
  - No finalizer; rely on callers to always call Destroy —
    rejected: Spec FR-013 / SC-003 require zeroing on
    reclamation as the defence-in-depth safety net.

### B4. Test for finalizer execution

- **Question:** How do we deterministically test that the
  finalizer ran (Spec User Story 3 / SC-003) given that
  `runtime.GC()` is best-effort?
- **Decision:** `TestSecureBytes_FinalizerZerosOnGC` allocates a
  `SecureBytes`, captures a "fired" flag through a side channel
  (a plain `bool` set by a test-only finalizer that wraps the
  real one), drops the strong reference, then calls
  `runtime.GC()` followed by `runtime.GC()` again (the standard
  "two GCs" pattern that gives finalizers from the previous
  cycle time to run). After the second GC, the test asserts the
  flag was set. The test does NOT inspect the buffer contents
  after GC (the backing array may have been reclaimed); it
  inspects the side-channel flag instead.
- **Rationale:** This is the canonical Go finalizer-test pattern
  used by the standard library (`runtime/mfinal_test.go`,
  `crypto/tls`). The two-GC sequence is documented in
  `runtime.SetFinalizer` and is reliable enough for CI; the test
  has a generous timeout to avoid flakes under loaded runners.
- **Alternatives considered:**
  - `runtime.GC()` + `runtime.Gosched()` only — rejected:
    flakier under load.
  - Inspecting buffer contents post-GC — rejected: by the time
    the finalizer has run and the test wakes, the backing array
    may have been recycled.

### B5. Sentinel-redaction test wiring

- **Question:** What slog handler captures output for
  `TestSecureBytes_RedactionSentinel`, and what byte sequence
  is the sentinel?
- **Decision:** Sentinel is the canonical
  `SECRET_SHOULD_NEVER_APPEAR_2` (per SDD-02 chunk-contract test
  list and `docs/TESTING-STRATEGY.md` §5 "sentinel pattern"). The
  test wires a `slog.JSONHandler` writing into a `bytes.Buffer`,
  emits a log entry containing the SecureBytes via
  `slog.Info("entry", "secret", sb)`, then asserts the buffer's
  bytes contain the literal `"[redacted]"` AND do NOT contain
  any byte of the sentinel. The same assertion is repeated for
  `fmt.Sprintf("%s", sb)`, `fmt.Sprintf("%v", sb)`, and the
  output of `json.Marshal(sb)`.
- **Rationale:** The test exercises every standard render path
  named by Spec FR-014 / FR-015 / FR-016. `slog.JSONHandler`
  with a buffer is the project's standard observability test
  pattern (per `docs/TESTING-STRATEGY.md` §5). Sentinel byte
  uniqueness ensures false-negatives are vanishingly unlikely.
- **Alternatives considered:**
  - Emit through `log/slog` text handler only — rejected:
    JSON-encoding is one of the three protected render paths
    and must be tested explicitly.
  - Use a unique random sentinel per test run — rejected: the
    project has standardised on the literal sentinel name for
    grep-ability across the test suite.

### B6. Empty-payload handling

- **Question:** Spec edge case "Empty payload" requires `New(nil)`
  and `New([]byte{})` to succeed. How is `mlock` of a zero-byte
  region handled?
- **Decision:** `New` allocates a freshly-made `[]byte` of length
  `len(b)`; if `len(b) == 0`, the buffer is `[]byte{}` (a non-nil
  zero-length slice). `unix.Mlock` is called on the slice; on
  both darwin and linux, locking a zero-byte region returns
  `nil` (no syscall is actually issued because the slice's
  underlying address can be `&zerobyte` from the runtime). The
  finalizer and Destroy paths handle a zero-length buffer
  correctly (the zero loop iterates zero times; `unix.Munlock`
  on a zero-length region also returns `nil`). `Use(fn)` invokes
  the callback with a zero-length buffer (Spec edge-case
  behaviour: callback is invoked with `len == 0`).
- **Rationale:** Spec edge case "Empty payload" demands this.
  Behaviour is the simplest correct one: degenerate cases follow
  the general rule.
- **Alternatives considered:**
  - Reject zero-length input — rejected: violates Spec edge case.
  - Skip the mlock call entirely for zero-length input —
    rejected: would create a special case in the constructor and
    in the finalizer; current behaviour costs nothing extra.

### B7. Mlock failure handling

- **Question:** If `unix.Mlock` fails (e.g. `RLIMIT_MEMLOCK`
  exhausted), what does `New` return? Does it fall back to
  unprotected memory?
- **Decision:** `New` returns `nil, err` where `err` wraps the
  `unix.Mlock` errno via `fmt.Errorf("hush/vault/securebytes: mlock: %w", err)`.
  Construction fails; no fallback to unprotected memory; no
  partial container is returned.
- **Rationale:** Spec FR-005 / SC-011 demand explicit failure on
  memory-protection denial. Falling back would silently weaken
  the security posture and would make the construction failure
  unobservable to callers.
- **Alternatives considered:**
  - Best-effort mlock with a warning log — rejected: violates
    FR-005; also `securebytes` has no logger (Constitution X
    says secret-handling packages should not log at all).

### B8. Concurrency invariants (formal)

- **Decision:** The mutex is a `sync.Mutex` (not RWMutex). It
  protects two fields: `buf []byte` (the buffer, nilled on
  destroy) and `destroyed bool`. `Len`, `Use`, `Destroy`,
  `LogValue`, `String`, `MarshalJSON` all take the mutex on
  entry. `Len` and the three render methods take it briefly
  (read-only, no blocking work inside). `Use` and `Destroy` hold
  it across blocking work (callback, syscall). The finalizer
  invokes `Destroy`, which is mutex-safe; finalizers run on a
  separate goroutine, so this matters.
- **Rationale:** Spec edge case "Concurrent borrow and destroy"
  requires destroy to NOT yank the buffer mid-borrow. Spec
  FR-008 requires concurrent borrows to be safe. The mutex
  satisfies both. Holding the mutex across the syscall is
  acceptable because `unix.Munlock` is microseconds — far below
  any user-visible threshold.
- **Alternatives considered:** **None** beyond B1's rejected
  RWMutex alternative.

### B9. Doc.go content for residual Go-runtime memory-copy risk

- **Decision:** Add a `doc.go` file with a package comment that:
  (a) summarises the package's purpose in one sentence, (b)
  documents the borrow contract for `Use`, (c) explicitly
  surfaces the documented residual risk from
  `docs/SECURITY.md` Layer 5 + §6 (Go runtime may transiently
  copy heap objects during GC compaction; mlock pins the
  current backing region but cannot prevent a transient copy
  in pathological cases). The note explicitly states this is
  outside the package's threat model (commodity malware enumerating
  dotfiles, NOT root-level memory forensics) and that no
  bandaid mitigation is added.
- **Rationale:** The user-supplied plan prompt and the SDD-02
  contract explicitly require this disclosure. Documenting it
  in `doc.go` (rather than in a README or in source comments
  scattered through `securebytes.go`) makes it appear in
  `go doc` output and on pkg.go.dev (or the equivalent local
  doc tool), which is where reviewers and consumers look.
- **Alternatives considered:**
  - Bury the note in `securebytes.go` — rejected: less visible.
  - Omit it — rejected: violates the explicit user-supplied
    requirement and `docs/SECURITY.md` §6.

---

## C. Test strategy notes (locked by SDD-02)

The following are not research questions but reminders of what the
chunk contract demands; they are recorded here so Phase 1 can
reference them.

- **Unit tests** (table-driven where applicable; deterministic):
  - `TestSecureBytes_New_CopiesAndZeroesInput` — copy semantics +
    input-buffer zeroing (FR-003, FR-004).
  - `TestSecureBytes_Use_DeliversPayload` — borrow callback
    receives exact original bytes; concurrent borrows safe; panic
    in callback leaves container live (Spec edge cases).
  - `TestSecureBytes_Destroy_ZeroesAndIdempotent` — buffer zeroed
    after destroy; double-destroy is a no-op (FR-010, FR-011).
  - `TestSecureBytes_PostDestroy_ReturnsErrDestroyed` — `Use` on
    destroyed container returns `ErrDestroyed`; `Len` reports 0
    (FR-009, FR-012, FR-018).
  - `TestSecureBytes_Render_RedactsAllPaths` — slog, fmt.Stringer,
    json.Marshaler all return `[redacted]` for live AND destroyed
    containers (FR-014, FR-015, FR-016, FR-017).
- **Sentinel-leak test:** `TestSecureBytes_RedactionSentinel` —
  wraps `SECRET_SHOULD_NEVER_APPEAR_2`, exercises every render
  path through a `slog.JSONHandler` writing into a buffer, asserts
  sentinel never appears anywhere in captured output (per
  `docs/TESTING-STRATEGY.md` §5).
- **Finalizer test:** `TestSecureBytes_FinalizerZerosOnGC` — drops
  reference, forces GC, asserts the finalizer ran via a side-
  channel flag (Spec User Story 3 / SC-003).
- **Race test:** `TestSecureBytes_ConcurrentUse` — N goroutines
  invoking `Use` concurrently against the same container; clean
  under `go test -race`.
- **Coverage:** 100% via `go test -cover
  ./internal/vault/securebytes/` (Constitution VIII).
- **Fuzz target:** **None** — the package has no parser surface.
  Cross-checked against `docs/TESTING-STRATEGY.md` §2 fuzz list
  (file/JWT/ECIES/signature/TOML/socket-JSON parsers); none of
  those are in this package.

---

## D. Summary: every NEEDS-CLARIFICATION resolved

| # | Question | Resolution |
|---|----------|-----------|
| B1 | Mutex hold strategy in `Use` | Mutex held for the entire callback duration. |
| B2 | Volatile-style zero write | `for i := range buf { buf[i] = 0 }` + `runtime.KeepAlive(sb)` + slice-header nil. |
| B3 | Finalizer wiring | `runtime.SetFinalizer(sb, (*SecureBytes).finalize)`; method-value form, no closure cycle. |
| B4 | Finalizer test pattern | Side-channel flag set by a test-only finalizer wrapper; two `runtime.GC()` calls. |
| B5 | Sentinel-redaction test wiring | `slog.JSONHandler` into `bytes.Buffer`; sentinel `SECRET_SHOULD_NEVER_APPEAR_2`; assert presence of `[redacted]` and absence of sentinel. |
| B6 | Empty-payload | Allowed; degenerate cases follow general rule; mlock/munlock of zero-length region is a no-op. |
| B7 | Mlock failure | `New` returns `nil, err` wrapping the syscall error; no fallback to unprotected memory. |
| B8 | Concurrency invariants | `sync.Mutex` protects `buf` and `destroyed`; `Use` and `Destroy` hold it across blocking work. |
| B9 | Residual-risk disclosure | `doc.go` documents the Go-runtime memory-copy risk; no mitigation added. |

No `[NEEDS CLARIFICATION]` markers remain.
