# Quickstart — `internal/vault` (SDD-03)

This is a consumer-side recipe. Run the snippets in your head against
the locked exported API in `contracts/vault-api.md` and you will see
how `internal/vault` plugs into the rest of the project.

> **Audience:** the implementers of SDD-10 (server startup +
> SIGHUP reload), SDD-13 (server secret-fetch handler), SDD-17
> (`hush secret` rotation CLI), and SDD-25 (lifecycle harness).
>
> **Prerequisite:** you have an `internal/keys`-derived 32-byte AES
> key wrapped in a `*securebytes.SecureBytes` (SDD-01 → SDD-02).

---

## 1. Save a list of secrets to disk

```go
import (
    "context"
    "log/slog"
    "os"

    "github.com/mrz1836/hush/internal/vault"
    "github.com/mrz1836/hush/internal/vault/securebytes"
)

// vaultKey: 32-byte AES-256 key derived via internal/keys
// (BIP32 path m/44'/7743'/1'). Owned by the caller; this package
// does not Destroy it.
var vaultKey *securebytes.SecureBytes

// Construct the input list. Each Value is owned by the caller.
anthropic, err := securebytes.New([]byte("sk-ant-..."))
if err != nil { return err }
defer anthropic.Destroy()

github, err := securebytes.New([]byte("ghp_..."))
if err != nil { return err }
defer github.Destroy()

secrets := []vault.Secret{
    {Name: "ANTHROPIC_API_KEY", Description: "claude.ai API key", Value: anthropic},
    {Name: "GITHUB_TOKEN",      Description: "PAT for ci",        Value: github},
}

// Save commits atomically. The parent directory's mode is verified
// to be exactly 0700; the produced file's mode is exactly 0600.
if err := vault.Save(context.TODO(), "/Users/op/.hush/secrets.vault", vaultKey, secrets); err != nil {
    slog.Error("vault save failed", "err", err) // err.Error() never contains a secret value
    return err
}
```

### What can go wrong on `Save`

| Error | Meaning | Caller remediation |
|-------|---------|---------------------|
| `ErrDuplicateName` | The same `Name` appeared twice in `secrets` | Caller bug; deduplicate the input. No filesystem touch occurred. |
| `ErrInvalidName` | A `Name` or `Description` violated the FR-008 constraints | Caller bug; sanitise the input. No filesystem touch occurred. |
| `ErrFilePermsLoose` | Parent directory mode != 0700 | Operator: `chmod 700 ~/.hush`. |
| Wrapped `os.PathError` | I/O failure during write, sync, or rename | Diagnose the underlying I/O failure; the working file (if any) has been best-effort removed. |

`Save` does not Destroy the `*SecureBytes` containers in `secrets`;
the caller continues to own their lifetimes. The recommended pattern
is `defer container.Destroy()` per container at the construction
site (as in the snippet above).

---

## 2. Load the vault and serve secrets to consumers

```go
import (
    "context"
    "log/slog"

    "github.com/mrz1836/hush/internal/vault"
    "github.com/mrz1836/hush/internal/vault/securebytes"
)

// vaultKey owned by the caller (e.g. derived once at startup and
// cached for the duration of the process).
var vaultKey *securebytes.SecureBytes

store, err := vault.Load(context.TODO(), "/Users/op/.hush/secrets.vault", vaultKey)
if err != nil {
    // Every error is one of the typed sentinels in contracts/vault-api.md.
    // err.Error() never contains a secret value.
    slog.Error("vault load failed", "err", err)
    return err
}
defer store.Destroy() // idempotent; zeroes every internally-held SecureBytes
```

### Per-request retrieval (server's secret-fetch handler shape)

```go
sb, err := store.Get("ANTHROPIC_API_KEY")
if err != nil {
    // ErrSecretNotFound: the requested name is not in this vault.
    // ErrStoreDestroyed: the store has been Destroyed (e.g. SIGHUP
    //                     swap with a tiny race; caller should retry
    //                     against the new store published by SDD-10).
    return err
}
defer sb.Destroy()                         // caller owns the returned container

// Use the payload via the borrow callback. The slice MUST NOT be
// retained beyond the callback.
err = sb.Use(func(plain []byte) {
    // ECIES-encrypt `plain` into the response body, etc.
    // The plaintext does not escape this scope.
})
```

`store.Get` returns a **fresh** `*SecureBytes` per call. Destroying
the returned container has no effect on `store`'s internal copy
nor on any other consumer's view of the same name. This is the
property that lets the server's secret-fetch handler hand out
per-request containers and `Destroy` them on response without
breaking concurrent fetches.

---

## 3. Enumerate held names (rotation CLI shape)

```go
names := store.Names() // defensive copy; safe to mutate
slog.Info("vault contents", "names", names) // no secret values logged

for _, n := range names {
    // operate on the name, e.g. prompt the operator for a new value
}
```

`store.Names()` returns the names in their stable file-load order
(spec FR-025). The slice is freshly copied per call, so callers can
sort or filter it without affecting any other observer.

---

## 4. SIGHUP reload pattern (sketch — owned by SDD-10)

This package provides the load primitive that SDD-10's atomic-pointer
swap is built on. The skeleton is included here so consumers see how
their own code will compose against the locked surface.

```go
type liveVault struct {
    p atomic.Pointer[vault.Store]
}

func (lv *liveVault) replaceWith(ctx context.Context, path string, key *securebytes.SecureBytes) error {
    next, err := vault.Load(ctx, path, key)
    if err != nil {
        return err // typed sentinel; caller logs and skips the swap
    }
    prev := lv.p.Swap(&next)
    // Wait for in-flight requests to drain (SDD-10's responsibility),
    // then destroy the previous store.
    if prev != nil {
        _ = (*prev).Destroy()
    }
    return nil
}
```

The contract this snippet relies on:

- `vault.Load` returns a fully-constructed `Store` or a typed error
  — never a partially-constructed `Store`.
- `Store.Destroy` is idempotent and safe to call after the pointer
  has been swapped out.
- `Store.Get` on a destroyed store returns `ErrStoreDestroyed` — a
  caller racing the swap may observe this and retry against
  `lv.p.Load()` to pick up the new store.

---

## 5. What this package does NOT do

- Derive the encryption key from a passphrase. That is SDD-01's job
  (`keys.DeriveVaultEncKey` consumes the salt this package stores
  in the file's header field).
- Implement SIGHUP reload, atomic-pointer publication, or
  in-flight-request drain. That is SDD-10's job.
- Detect or clean up orphaned `<path>.tmp` files left behind by a
  killed save. That is the rotation CLI's startup sweep
  (SDD-17's job).
- Resolve symlinks at the target path. The target path is treated
  as a regular file managed by the project's own init flow.
- Lock the vault file against multi-writer access. That is the
  upstream PID-file / flock discipline (SDD-17's job).

If you find yourself writing one of these inside `internal/vault`,
stop — you are in the wrong package.
