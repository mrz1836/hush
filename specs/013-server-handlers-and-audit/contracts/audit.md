# Contract — `internal/audit` package surface

This contract is the SDD-13 lock on the `internal/audit` package's exported API. The package is the system's tamper-evident memory (Constitution III Layer 6); changes to its surface require a new SDD chunk.

---

## Exported types

```go
package audit

// Event is one record of the hash-chained, signed audit log. Serialised
// to disk verbatim as line-delimited canonical JSON.
type Event struct {
    Seq       uint64         `json:"seq"`
    Time      time.Time      `json:"time"`
    Action    string         `json:"action"`
    Data      map[string]any `json:"data,omitempty"`
    PrevHash  string         `json:"prev_hash"`
    Hash      string         `json:"hash"`
    Signature string         `json:"signature"`
}

// Writer is the producer-facing interface. Implementations are
// concurrency-safe; Append blocks under buffer pressure (FR-031).
type Writer interface {
    Append(ctx context.Context, action string, data map[string]any) error
    Run(ctx context.Context) error
}

// DiscordMirror is the optional best-effort chat-platform publisher.
// Pass nil to NewWriter to disable mirroring.
type DiscordMirror struct { /* unexported */ }

// MirrorSession is the narrow seam over *discordgo.Session that the
// mirror invokes. *discordgo.Session satisfies it structurally.
type MirrorSession interface {
    ChannelMessageSendComplex(channelID string, data *discordgo.MessageSend, opts ...discordgo.RequestOption) (*discordgo.Message, error)
}
```

## Exported functions

```go
// NewWriter constructs a Writer. Validates inputs synchronously; the
// long-lived goroutine starts on Run(ctx). When the chain file exists,
// NewWriter scans the last line to recover Seq and prevHash; when it
// does not exist, NewWriter starts the chain at Seq=1 with the genesis
// predecessor hash. mirror MAY be nil. logger MUST NOT be nil.
//
// Returns ErrInvalidPath, ErrInvalidKey, or ErrChainTailUnreadable on
// construction-time validation failures.
func NewWriter(
    ctx context.Context,
    path string,
    signKey *ecdsa.PrivateKey,
    mirror *DiscordMirror,
    logger *slog.Logger,
) (Writer, error)

// NewDiscordMirror constructs a DiscordMirror. An empty channelID
// disables the mirror entirely (FR-036). The session MAY be a
// *discordgo.Session in production or a test stub satisfying
// MirrorSession.
func NewDiscordMirror(channelID string, session MirrorSession) *DiscordMirror

// Verify reads the chain file end-to-end, recomputing every event's
// hash and verifying every event's signature against verifyKey. The
// first inconsistency surfaces ErrAuditChainBroken with the offending
// Seq carried via errors.As on a *ChainError value (exported helper).
//
// Verify is the offline-verification entry point; the writer goroutine
// itself never calls Verify (the writer trusts its in-process Seq /
// prevHash state).
func Verify(path string, verifyKey *ecdsa.PublicKey) error
```

## Exported sentinels

```go
// ErrAuditChainBroken indicates the on-disk chain failed integrity
// verification. errors.As against *ChainError to recover the offending
// Seq.
var ErrAuditChainBroken = errors.New("hush/audit: chain integrity broken")

// ErrShutdown is returned by Append when Run's ctx has been cancelled
// and the writer is no longer accepting events.
var ErrShutdown = errors.New("hush/audit: writer shut down")

// ErrChainTailUnreadable indicates the writer could not parse the last
// line of an existing chain file at NewWriter time. The chain is NOT
// silently truncated; the operator must intervene.
var ErrChainTailUnreadable = errors.New("hush/audit: chain tail unreadable")

// ErrInvalidPath indicates the supplied audit-log path is empty or
// outside the allowed state directory.
var ErrInvalidPath = errors.New("hush/audit: invalid log path")

// ErrInvalidKey indicates the supplied signing key is nil or
// not on the secp256k1 curve.
var ErrInvalidKey = errors.New("hush/audit: invalid signing key")
```

## Exported error helper

```go
// ChainError carries the Seq of the first inconsistent event surfaced
// by Verify. Wraps ErrAuditChainBroken so errors.Is(err,
// ErrAuditChainBroken) works for callers that don't care about the seq.
type ChainError struct {
    Seq    uint64
    Reason string // "hash_mismatch" | "signature_invalid" | "seq_gap" | "prev_hash_mismatch"
    Err    error
}

func (e *ChainError) Error() string { ... }
func (e *ChainError) Unwrap() error { return e.Err }
```

---

## Behavioural contract

### `Writer.Append(ctx, action, data)`

1. Returns `ErrShutdown` if `Run`'s ctx is already cancelled.
2. Returns the ctx error if the caller's `ctx` is already cancelled.
3. Validates `action != ""` (returns a typed validation error — TBD-named at implement time).
4. Synchronously rendezvouses with the writer goroutine via an unbuffered channel: the producer blocks until the writer goroutine has assigned a Seq, computed the hash, signed, and persisted the event to disk.
5. Returns the writer goroutine's persistence error verbatim if persistence fails (the producer's caller maps this to a 503 / 500 in the chassis).
6. On success, returns `nil`. The event is on-chain at return.

### `Writer.Run(ctx)`

1. Single-call lifecycle. A second call returns an `ErrAlreadyRun`-shaped error (TBD).
2. Spawns the single writer goroutine and (if mirror configured) the single mirror goroutine.
3. Emits `actionServerStart` as Seq 1 if the chain file is empty or new.
4. On `ctx.Done()`:
   - Writer goroutine drains the `accept` channel best-effort (every `pending` already in flight completes; new sends from `Append` immediately return `ErrShutdown`).
   - Emits `actionServerStop` as the final event (the chassis `Run` calls `Append("server_stop", ...)` immediately before cancelling the audit ctx; the writer goroutine processes it and then drains).
   - Calls `*os.File.Sync()` and `*os.File.Close()`.
   - Mirror goroutine drains its buffer for up to `mirrorShutdownTimeout` (default 5s), then exits whether or not the buffer drained.
5. Returns `nil` on clean shutdown, or a wrapped error if any persistence step failed during drain.

### Mirror discipline

1. The mirror goroutine reads from a buffered channel of size `64`.
2. On a successful disk persist, the writer goroutine attempts a non-blocking send to the mirror channel: `select { case ch <- ev: default: WARN-log + continue }`.
3. The mirror goroutine calls `session.ChannelMessageSendComplex(channelID, msg)`.
4. On error, logs WARN with `seq` + `action` + error class string only. Never the bot token, never the event's signature.
5. No retries. A single-event mirror failure does NOT inflate the next event's mirror dispatch.
6. `actionAuditMirrorFailed` audit events are emitted FROM THE MIRROR GOROUTINE via `Append` so the chain reflects the mirror degradation. (Loop concern: a mirror failure on the `actionAuditMirrorFailed` event itself is allowed to drop silently — the on-disk chain still records the original failure event.)

### Concurrency invariants

- The on-disk file handle has EXACTLY ONE owner: the writer goroutine.
- The `Seq` counter and `prevHash` byte slice have EXACTLY ONE owner: the writer goroutine.
- The mirror channel has EXACTLY ONE consumer: the mirror goroutine.
- Producers MAY call `Append` from any goroutine; the rendezvous channel serialises them. Concurrent producers are queued by the Go runtime's channel-fairness contract.

---

## Constitution-locked invariants

1. **No configuration knob causes events to be dropped under backpressure.** (FR-031, FR-045.) The on-disk channel is unbuffered (rendezvous-based); buffer-full simply means producers wait.
2. **Hash chain is contiguous from Seq=1.** (FR-022.) `NewWriter` recovers from the last on-disk line; the recovery path is itself tested.
3. **Hash covers `(prevHash || canonicalJSON(Event-without-Hash-or-Signature))`.** (FR-023, FR-026.)
4. **Signature verifies against the BIP32-derived audit key.** (FR-024.) The signing key is a `*ecdsa.PrivateKey` injected at `NewWriter`-time; the package itself does NOT derive the key.
5. **Verify-failure surfaces `ErrAuditChainBroken` at the first inconsistent Seq.** (FR-025; SC-008.)
6. **Mirror failures NEVER block, retry indefinitely, or insert compensating events into the on-disk chain.** (FR-035, FR-037.)
7. **`Data` payloads NEVER carry secret values, JWT bytes, signature bytes, nonce bytes, bot tokens, or the audit signing key.** (FR-028, FR-029, FR-030.) Sentinel-leak test (`TestAudit_RecordNoSecretValue`) asserts `SECRET_SHOULD_NEVER_APPEAR_13` is absent from every on-disk event.

---

## On-disk file format

```
{"seq":1,"time":"2026-04-30T18:23:11.123456789Z","action":"server_start","data":{...},"prev_hash":"<64 hex>","hash":"<64 hex>","signature":"<base64>"}\n
{"seq":2,"time":"2026-04-30T18:24:01.987654321Z","action":"claim_outcome","data":{...},"prev_hash":"<64 hex>","hash":"<64 hex>","signature":"<base64>"}\n
...
```

- Line-delimited canonical JSON (one Event per line; key order alphabetical; same encoding as the canonical preimage).
- File mode `0600` (FR-015).
- `O_APPEND|O_CREATE|O_WRONLY` open mode; never seeks; never truncates.
- `bufio.Scanner` with `MaxScanTokenSize = 1 MiB` for read-side `Verify`.

---

## Backward compatibility

This is the v0.1.0 contract for `internal/audit`. SDD-13 is the lock point. Future SDDs MAY:

- Add new methods to `Writer` (additive).
- Add new sentinel errors (additive).
- Add new fields to `Event` ONLY if the new field is included in the canonical preimage AND a chain-version bump is documented in `docs/SECURITY.md` Layer 6. Adding a field without bumping the version would silently invalidate every existing chain.

A field rename, a hash function change, or a signature scheme change requires a constitutional amendment AND a chain-rotation procedure (start a new chain file with a versioned genesis).
