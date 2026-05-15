# Phase 1 Data Model — internal/vault/securebytes (SDD-02)

`internal/vault/securebytes` defines exactly one in-process,
reference-typed entity: the `SecureBytes` container. It has no
persisted state, no on-disk representation, and no wire format. The
"data model" here is:

- the typed surface across the package's exported boundary,
- the container's internal state and its single state-machine
  transition,
- the lifetime / ownership rules,
- the secret-versus-public classification of every value that
  flows in or out (Constitution X: secret material MUST NOT cross
  any logging path or be converted to `string` inside this
  package).

No struct types beyond the opaque container are defined or
persisted. Callers own the input buffers; the container owns the
copy.

---

## Inputs

### Input buffer

| Field | Value |
|-------|-------|
| Go type | `[]byte` (parameter to `New`) |
| Constraints | Any length ≥ 0. `nil` and `[]byte{}` both produce a valid zero-length container. |
| Validation rule | None at the constructor. (Length is unrestricted; behaviour for the OS denying mlock is FR-005, surfaced as a wrapped error.) |
| Secret? | **Yes.** Treated as secret material. The constructor copies the contents into a fresh, mlocked buffer and zeros the caller's buffer before returning (FR-004 / SC-006). |
| Lifetime | Caller-allocated; on return from `New`, the caller's buffer contains only zero bytes. The caller may immediately discard their reference. |
| Source | The caller (e.g. `internal/keys` returns a 64-byte master seed; `hush init` reads a passphrase from the OS keychain into `[]byte`). Sources are out-of-scope for this package. |

### Borrow callback

| Field | Value |
|-------|-------|
| Go type | `func(b []byte)` (parameter to `Use`) |
| Constraints | Caller contract: MUST NOT retain `b` past the function return. Cannot be enforced at the language level; documented in `doc.go`. A callback that retains the slice creates a caller bug, NOT a container failure. |
| Side effects allowed | Read of `b`. Mutation of `b` is undefined behaviour from the package's contract perspective (the spec does not promise the buffer is read-only, but the security guarantees presume the buffer is read-only for the duration of the borrow); future hardening may copy-on-borrow if a downstream consumer needs the stronger guarantee. |
| Panic semantics | A panic from the callback does NOT leave the container in a corrupted state. The container's mutex is released (deferred), and subsequent callers may borrow, destroy, or render. Spec edge case "Borrow callback that panics". |
| Lifetime | Per-call. The package retains no reference to the callback. |

---

## Outputs

### Container reference

| Field | Value |
|-------|-------|
| Go type | `*SecureBytes` (return of `New`) |
| Length | Internal `len(buf)` matches `len(input)` from `New`. Exposed via `Len()` (returns 0 after destroy). |
| Determinism | Not deterministic — the container holds the bytes given to it. Two `New` calls with identical inputs produce two distinct containers, each with its own mutex and finalizer. |
| Secret? | **The reference itself is non-secret** (it's a pointer). The bytes it wraps are secret. |
| Lifetime | Owned by the caller for live containers; transitions to "owned by the runtime, awaiting reclamation" if the caller drops the reference without calling `Destroy`. |
| Distinctness | Each `*SecureBytes` is a distinct allocation; pointer comparison distinguishes them. |

### Length

| Field | Value |
|-------|-------|
| Go type | `int` (return of `Len`) |
| Range | `0 ≤ n ≤ len(input)`. `0` after destroy. |
| Secret? | **No.** Length is metadata, not payload, and is exposed by FR-018. |
| Lifetime | Returned by value. |

### Render outputs

| Field | Value |
|-------|-------|
| `LogValue() slog.Value` | Always returns `slog.StringValue("[redacted]")`. |
| `String() string` | Always returns `"[redacted]"`. |
| `MarshalJSON() ([]byte, error)` | Always returns `[]byte("\"[redacted]\""), nil`. |
| Secret? | **No.** All three return the literal `[redacted]` — non-secret by construction. |
| Determinism | Identical output before and after destruction (FR-017). |
| Lifetime | Returned by value (or `[]byte`-by-value for JSON). |

---

## Internal state (NOT exported)

These fields are part of the package's internal contract and are
documented here to make the state machine explicit. They are
mentioned in `data-model.md` so reviewers understand the lifecycle;
they are NOT part of the public API and MUST NOT be referenced from
outside the package.

| Field | Type | Role |
|-------|------|------|
| `mu` | `sync.Mutex` | Guards `buf` and `destroyed` across the blocking borrow callback in `Use` and the syscall in `Destroy`. |
| `buf` | `[]byte` | The mlocked, zero-on-destroy payload. Set in `New`; replaced with a zero loop and then nilled in `Destroy`. |
| `destroyed` | `bool` | `false` while live; `true` after the first `Destroy`. Render methods do NOT consult this flag. |

---

## Errors

### `ErrDestroyed`

- **Type:** `error` (sentinel; package-level `var = errors.New("hush/vault/securebytes: destroyed")`).
- **Returned by:** `Use`. (`Destroy` is idempotent and returns `nil` on the second call.)
- **Trigger:** `destroyed == true` at entry to `Use`.
- **Detection by callers:** `errors.Is(err, securebytes.ErrDestroyed)`.
- **Leakage:** error message identifies the failure mode and the package path only. Carries no payload bytes, no length, no other diagnostic.

### Wrapped syscall errors (from `Destroy` / `New`)

- **Type:** `error` formed via `fmt.Errorf("hush/vault/securebytes: <op>: %w", err)`. Underlying error is a `syscall.Errno` from `unix.Mlock` or `unix.Munlock`.
- **Returned by:** `New` (on `mlock` failure — Spec FR-005 / SC-011) and `Destroy` (rare; `munlock` failure on a malformed buffer pointer, which should never happen in practice).
- **Detection by callers:** `errors.Is(err, syscall.EAGAIN)`, `errors.Is(err, syscall.ENOMEM)`, etc., or `errors.As` to extract the `syscall.Errno`.
- **Leakage:** none.

---

## State machine

The container has exactly two states and one transition:

```
                    New(b []byte)
                         │
                         ▼
                ┌────────────────┐
                │     LIVE       │
                │  (mlocked,     │
                │   zero-on-     │
                │   destroy      │
                │   pending)     │
                └────────────────┘
                  │           │
                  │           │
        Destroy() │           │ Finalizer
        (explicit)│           │ (GC-triggered)
                  │           │
                  ▼           ▼
                ┌────────────────┐
                │   DESTROYED    │
                │ (buf zeroed,   │
                │  munlocked,    │
                │  destroyed=    │
                │  true)         │
                └────────────────┘
                  │
                  │ Destroy() (idempotent — no-op)
                  │
                  └──► DESTROYED (unchanged)
```

State-transition contract:

| From → To | Trigger | Side effects |
|-----------|---------|--------------|
| `(none) → LIVE` | `New(b)` returns successfully | Allocate fresh buffer; copy `b`; `mlock` the buffer; zero the caller's `b`; register finalizer. |
| `LIVE → DESTROYED` | `Destroy()` first call OR finalizer | Acquire mutex; zero `buf`; `munlock`; nil `buf`; set `destroyed = true`; release mutex; `runtime.KeepAlive(sb)`. |
| `DESTROYED → DESTROYED` | `Destroy()` subsequent call | Mutex acquired and immediately released; no work performed; returns `nil`. |
| `LIVE → LIVE` | `Use(fn)` | Mutex acquired; callback invoked with `buf`; mutex released. No state change. |
| `DESTROYED → DESTROYED` | `Use(fn)` | Mutex acquired; `destroyed == true` observed; mutex released; returns `ErrDestroyed`. Callback NOT invoked. |
| (no state change) | `Len()`, `LogValue()`, `String()`, `MarshalJSON()` | Read-only. The render methods do not consult `destroyed` (FR-017). `Len` consults it (returns 0 after destroy per FR-018). |

There is no `LIVE → LIVE'` transition — the container is immutable once constructed (Spec "Out of Scope: Resizing or appending to a container after construction").

---

## Lifecycle invariants

These invariants hold across every public operation and are
test-enforced (per the contract in `contracts/securebytes-api.md`):

1. **Buffer-pointer invariant:** While `destroyed == false`, `buf`
   is non-nil (it may be zero-length) and points to mlocked memory.
   While `destroyed == true`, `buf == nil`.
2. **Render-stability invariant:** `LogValue`, `String`,
   `MarshalJSON` produce identical outputs before and after
   destruction (FR-017).
3. **No-leak invariant:** No public method returns the buffer
   contents or the buffer pointer to the caller. The only path that
   exposes the bytes is the `Use` callback, and that callback's
   contract forbids retention.
4. **Idempotency invariant:** `Destroy` may be called any number of
   times; only the first call performs the zero+munlock; subsequent
   calls return `nil` immediately.
5. **Reclamation-safety invariant:** A `*SecureBytes` that becomes
   unreachable without a prior explicit `Destroy` has its finalizer
   trigger `Destroy` before the backing array is recycled (Spec
   FR-013 / SC-003).

---

## Ownership rules

- **Caller owns `b` before `New`** — fully theirs.
- **Caller owns nothing after `New`** — `b`'s contents are zeroed;
  the container owns the only live copy.
- **Caller owns the `*SecureBytes` reference until `Destroy`** —
  they hold the only public handle. They MAY pass the reference to
  helpers; helpers MUST NOT call `Destroy` on a borrowed reference
  unless ownership was explicitly transferred (a project-wide
  convention; not enforceable at the language level).
- **Container owns its buffer through both Destroy paths** —
  whether explicit `Destroy()` or finalizer-triggered, the
  container handles the zero+munlock+nil sequence under its own
  mutex.
- **Borrow callback owns `b` for the duration of the call only** —
  retention is a caller bug.

---

## Out-of-scope (consumer-side) data shapes

The following are NOT defined here; they are the consumer's
responsibility:

- The container's source data (passphrase, derived keys, JWT
  signing keys, ECIES envelope keys, decrypted secret values).
- Persistence formats (vault file format owns these — SDD-03).
- Audit-log entries (SDD-05 owns redaction patterns for these).
- Any constant-time comparison of two containers (Spec "Out of
  Scope"; deferred to a future helper if a consumer needs it).
