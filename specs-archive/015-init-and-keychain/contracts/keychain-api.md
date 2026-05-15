# Contract — `internal/keychain` (locked at SDD-15)

This is the authoritative API surface for the new `internal/keychain`
package. Anything not listed here is implementation detail and may
change without notice.

**Package path**: `github.com/mrz1836/hush/internal/keychain`

**Importers (locked direction)**:
- `internal/cli` (init.go) — primary consumer
- _no other internal package may import `internal/keychain`_; if
  another package needs keychain semantics, it goes through `cli` or
  a future facade

---

## 1. Interface

```go
// Keychain is the platform-agnostic OS keychain operations contract.
// All operations are atomic at the OS layer (the underlying keychain
// service handles concurrent access — Keychain implementations do
// NOT add any internal serialization).
//
// Implementations are safe for concurrent use only insofar as the
// underlying OS keychain service is; callers should serialize
// per-(service, account) operations themselves.
type Keychain interface {
    // Store creates a new keychain item under (service, account) with
    // the supplied secret value and the supplied per-binary ACL.
    //
    // The acl argument is the absolute path of the binary that will
    // be authorized to read the item (typically resolved via
    // os.Executable() at the call site). On macOS this becomes the
    // `-T <acl>` flag to `security add-generic-password`. On
    // platforms without per-binary ACL semantics, implementations
    // MUST return an error (typically by being unreachable — see
    // PerBinaryACLSupported).
    //
    // Store reads the secret bytes via SecureBytes.Use(fn); the slice
    // passed to fn MUST NOT be retained beyond the call.
    //
    // Returns ErrKeychainItemExists if an item already exists under
    // (service, account). Callers that want overwrite semantics MUST
    // call Delete first.
    Store(ctx context.Context, service, account string, value *securebytes.SecureBytes, acl string) error

    // Retrieve fetches the secret value for (service, account) into a
    // fresh *securebytes.SecureBytes that the caller owns and must
    // Destroy.
    //
    // Returns ErrKeychainItemNotFound if no item exists; returns
    // ErrKeychainPermissionDenied if the OS denied access (typically
    // because the caller is not the binary named in the item's ACL).
    Retrieve(ctx context.Context, service, account string) (*securebytes.SecureBytes, error)

    // Delete removes the item at (service, account). Returns
    // ErrKeychainItemNotFound if no item exists. The operation is
    // idempotent only at the call-site level — callers that want
    // "delete-if-exists" should errors.Is the result.
    Delete(ctx context.Context, service, account string) error
}
```

---

## 2. Constructor

```go
// New returns the platform-native Keychain implementation. Returns
// the macOS implementation on darwin (which shells out to
// /usr/bin/security) and the Linux Secret Service implementation on
// linux (zalando/go-keyring), with the latter being build-only —
// callers in init.go MUST gate with PerBinaryACLSupported() and
// refuse on platforms where it returns false.
//
// logger is used for operational tracing only; no secret value is
// ever passed to the logger.
func New(logger *slog.Logger) (Keychain, error)
```

The constructor returns the **interface** (not a concrete type)
because the platform implementation is selected at construction time
and the caller has no need to distinguish; this is the single
exception to Constitution IX's "accept interface, return concrete"
guideline, justified because the concrete type is build-tag-gated and
not nameable from cross-platform code.

---

## 3. Capability probe

```go
// PerBinaryACLSupported reports whether the current platform's
// keychain implementation honours the `acl` argument as a per-binary
// access restriction. Returns true on darwin, false on linux.
//
// init.go MUST call this before any Store invocation and MUST refuse
// to write any keychain item, vault file, or config file when it
// returns false (FR-020a — no silent ACL downgrade).
func PerBinaryACLSupported() bool
```

---

## 4. Sentinel errors

```go
// ErrKeychainItemNotFound is returned by Retrieve and Delete when no
// item exists for (service, account).
var ErrKeychainItemNotFound = errors.New("hush/keychain: item not found")

// ErrKeychainItemExists is returned by Store when an item already
// exists for (service, account). The caller (init.go) maps this to
// errKeychainItemExists and exits non-zero per FR-012 / Clarification
// 2026-05-03 Q1.
var ErrKeychainItemExists = errors.New("hush/keychain: item already exists")

// ErrKeychainPermissionDenied is returned by Retrieve when the OS
// keychain service denied access (typically because the caller is
// not the binary named in the item's ACL).
var ErrKeychainPermissionDenied = errors.New("hush/keychain: permission denied")

// ErrKeychainUnsupportedPlatform is returned by Store on platforms
// without per-binary ACL semantics. init.go SHOULD NOT see this in
// practice because it gates with PerBinaryACLSupported(); the error
// exists as defense-in-depth.
var ErrKeychainUnsupportedPlatform = errors.New("hush/keychain: per-binary ACL unsupported on this platform")
```

---

## 5. Test seam: `FakeKeychain`

```go
// FakeKeychain is an in-process Keychain implementation backed by a
// map[string]storedItem. Test-only — production code MUST NOT
// import or instantiate this type.
//
// Construction:
//   kc := keychain.NewFake()
//   defer kc.Destroy()
//
// Behaviour:
//   - Store fails with ErrKeychainItemExists if (service, account) is occupied.
//   - Retrieve returns a fresh *securebytes.SecureBytes per call.
//   - Delete is non-idempotent (mirrors Keychain.Delete semantics).
//   - The supplied `acl` is recorded so tests can assert that init
//     passed the absolute binary path through.
type FakeKeychain struct{ /* unexported */ }

func NewFake() *FakeKeychain
func (f *FakeKeychain) Destroy() // zeroes all stored *securebytes.SecureBytes
func (f *FakeKeychain) RecordedACL(service, account string) string // for assertions
```

---

## 6. Darwin implementation behaviour (for reference)

`Store` constructs an `*exec.Cmd`:

```text
/usr/bin/security add-generic-password
    -s <service>          -- the service name (e.g. "hush-discord")
    -a <account>          -- the account (e.g. "hush-server")
    -T <acl>              -- per-binary ACL (absolute path to running hush binary)
    -w                    -- read password from stdin (NOT argv — avoids ps leakage)
```

Behaviour invariants enforced by `keychain_darwin.go`:

- The `-w` flag is **always** present so the secret never appears in
  argv (and therefore never in `ps`/`/proc/PID/cmdline`).
- The secret is written to `cmd.Stdin` from inside a
  `value.Use(func(b []byte) { cmd.Stdin = bytes.NewReader(b) })`
  closure; the closure does not retain the slice past `cmd.Run`.
- No `-A` (allow-all) flag is ever passed.
- No `-U` (update existing) flag is ever passed; collisions surface
  as `ErrKeychainItemExists` so init can refuse.

`Retrieve` runs `/usr/bin/security find-generic-password -s <s> -a <a> -w`
and parses the trailing newline-terminated password from stdout into
a fresh `*securebytes.SecureBytes`. Empty stdout → `ErrKeychainItemNotFound`.
Exit code 44 (`SecKeychainErrItemNotFound`) → `ErrKeychainItemNotFound`.
Exit code 51 (user cancelled) → `ErrKeychainPermissionDenied`.

`Delete` runs `/usr/bin/security delete-generic-password -s <s> -a <a>`
and translates exit codes the same way.

---

## 7. Linux implementation behaviour (for reference)

`Store` calls `keyring.Set(service, account, secretAsString)` from
zalando/go-keyring. The `acl` argument is **discarded** — Linux Secret
Service has no per-binary ACL primitive (research §2). This is
acceptable because `init.go` calls `PerBinaryACLSupported()` first and
refuses on Linux; production code never reaches Linux `Store`.

For tests that exercise `Retrieve`/`Delete` on Linux (e.g. so the
existing `serve.go` Linux retrieval path remains build-tested), the
implementation calls the matching `keyring.Get` / `keyring.Delete`.

`*securebytes.SecureBytes` → string conversion at the
zalando/go-keyring boundary is a known leakage hazard (Constitution X
warning about plain `string`); the conversion is confined to a
stack-local within `Store`'s `Use` closure and the resulting string
is never logged. zalando/go-keyring itself does not log the value.
This is documented as a residual risk in `docs/SECURITY.md` §6
(future amendment) and is not exercised by `init` in v0.1.0.

---

## 8. Stability

This contract is **locked** at SDD-15. Future SDDs may **add**
symbols (e.g. an `Update` method, additional sentinels), but MUST
NOT remove or alter the signatures above.
