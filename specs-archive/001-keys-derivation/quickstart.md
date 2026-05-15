# Quickstart — `internal/keys` (SDD-01)

This file shows the canonical wiring of `internal/keys` from the
boundary-code perspective. It is **descriptive of the locked API**
in [contracts/keys-api.md](./contracts/keys-api.md), not an
implementation guide for the package itself.

If you are reading this to consume the package (SDD-03 vault, SDD-07
JWT, SDD-08 request signing, SDD-09 ECIES, SDD-15 `hush init`), this
is your reference. If you are implementing the package, see
`spec.md`, `plan.md`, `research.md`, and `data-model.md` instead.

---

## 1. Bootstrap a fresh trusted host (`hush init` flow — SDD-15)

```go
import (
    "context"
    "fmt"

    "github.com/mrz1836/hush/internal/keys"
)

func bootstrap(ctx context.Context, passphrase, salt []byte) error {
    seed, err := keys.DeriveMasterSeed(ctx, passphrase, salt)
    if err != nil {
        return fmt.Errorf("master seed: %w", err)
    }
    defer secureZero(seed) // wrap in vault.SecureBytes — SDD-02 owns this.

    jwtKey, err := keys.DeriveJWTSigningKey(seed)
    if err != nil {
        return fmt.Errorf("jwt key: %w", err)
    }

    vaultEncKey, err := keys.DeriveVaultEncKey(seed)
    if err != nil {
        return fmt.Errorf("vault enc key: %w", err)
    }
    defer secureZero(vaultEncKey)

    auditKey, err := keys.DeriveAuditSigningKey(seed)
    if err != nil {
        return fmt.Errorf("audit key: %w", err)
    }

    _ = jwtKey
    _ = auditKey
    return nil
}
```

Notes for the consumer:

- `passphrase` is read from the macOS Keychain (`hush` entry) earlier
  in the boot sequence, never from env vars or argv.
- `salt` is read from the vault file's salt field (the vault format
  carries it as plaintext; SDD-03 owns the file format).
- `secureZero` is a placeholder — the real implementation will use
  `internal/vault.SecureBytes` (SDD-02). `internal/keys` itself
  cannot import that package (leaf-package rule); the wrapping
  happens in the caller.
- `errors.Is(err, keys.ErrPassphraseTooShort)` and
  `errors.Is(err, keys.ErrSaltMissing)` are the supported ways for
  callers to branch on validation failure.

---

## 2. Register a per-machine client key (`hush init --client` flow — SDD-15 / SDD-16)

```go
func registerClient(ctx context.Context, passphrase, salt []byte, machineIndex uint32) (string, error) {
    seed, err := keys.DeriveMasterSeed(ctx, passphrase, salt)
    if err != nil {
        return "", err
    }
    defer secureZero(seed)

    clientKey, err := keys.DeriveClientKey(seed, machineIndex)
    if err != nil {
        return "", err
    }

    fp := keys.PublicKeyFingerprint(&clientKey.PublicKey)
    // fp is a 16-char lowercase hex string. Print to TTY for the
    // operator to copy into the server's registered_client_keys.
    return fp, nil
}
```

Notes:

- The same `(passphrase, salt, machineIndex)` triple ALWAYS produces
  the same `fp`. Operator UX: when re-registering an existing
  machine, the displayed fingerprint must match what is already in
  the server config — that is the visual confirmation step
  described in User Story 4.
- `machineIndex` ranges across the full `uint32` space; the
  derivation does not silently truncate.

---

## 3. Sign an audit-log record (SDD-05 audit-writer flow)

```go
func signAuditEntry(seed []byte, payload []byte) ([]byte, error) {
    key, err := keys.DeriveAuditSigningKey(seed)
    if err != nil {
        return nil, err
    }
    digest := sha256.Sum256(payload)
    return ecdsa.SignASN1(rand.Reader, key, digest[:])
}
```

The returned `*ecdsa.PrivateKey` is a Go-stdlib type, so any code
path that uses `crypto/ecdsa` (audit signing, JWT signing in SDD-07)
works against it directly without an adapter.

---

## 4. Validation failure paths

```go
import "errors"

seed, err := keys.DeriveMasterSeed(ctx, []byte("short"), nil)
switch {
case errors.Is(err, keys.ErrPassphraseTooShort):
    // < 12 bytes — operator typed too little. Fail fast, no KDF.
case errors.Is(err, keys.ErrSaltMissing):
    // salt missing or != 16 bytes — vault file likely corrupt.
case errors.Is(err, context.Canceled),
     errors.Is(err, context.DeadlineExceeded):
    // ctx was cancelled BEFORE entry. After entry, Argon2id runs
    // to completion regardless.
case err != nil:
    // No other error class is documented for v0.1.0; if you see one,
    // an upstream contract drifted.
default:
    _ = seed
}
```

`< 100ms` latency is guaranteed for the validation paths
(`ErrPassphraseTooShort`, `ErrSaltMissing`, and pre-cancelled ctx) —
they all return before Argon2id runs.

---

## 5. What this package does NOT do

- It does **not** read or write files (no `~/.hush/...` access).
- It does **not** allocate `SecureBytes` — the caller does.
- It does **not** call `crypto/rand` — the caller supplies all
  entropy via `passphrase` + `salt`.
- It does **not** log. No logger is imported. Errors carry the
  failure mode (sentinel values), not input bytes.
- It does **not** persist any derived material. Every output flows
  back to the caller and is the caller's responsibility from the
  return point onward.

If your consumer code expects any of those behaviours, the
expectation is wrong — the responsibility lives in a different
package (vault, logging, or the caller's bootstrap glue).

---

## 6. Verifying the wiring locally

After SDD-01 lands, the smoke test for a consumer is:

```bash
# from repo root
magex format:fix && magex lint && magex test:race
go test -fuzz=FuzzDeriveMaster -fuzztime=60s ./internal/keys/
go test -cover ./internal/keys/         # expect 100.0%
```

A green run on all four commands is the contract that downstream
SDDs can rely on. If any of them fails, fix `internal/keys` before
consuming it from downstream code.
