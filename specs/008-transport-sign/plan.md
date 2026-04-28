# Implementation Plan: `internal/transport/sign` — Canonical-JSON Sign/Verify + Nonce + Timestamp (SDD-08)

**Branch**: `008-transport-sign` | **Date**: 2026-04-28 | **Spec**: [spec.md](./spec.md)
**Input**: Feature specification from `/specs/008-transport-sign/spec.md`
**Chunk contract**: [docs/sdd/SDD-08.md](../../docs/sdd/SDD-08.md)

## Summary

`internal/transport/sign` is the leaf primitives package that gives every
signed client request a single, deterministic protocol: a canonical JSON
form of the payload, an ECDSA signature over the SHA-256 digest of that
canonical form, and a nonce + timestamp pair that defeats replay. It
backs Layer 4 of the seven-layer security stack (`docs/SECURITY.md` §3
Layer 4) and is consumed by the server's `/claim` and `/revoke` paths
(SDD-12) and by `hush request` on the client side (SDD-16). Both
consumers receive the same exported surface and the same set of
sentinel errors; honest requests round-trip iff both sides agree on
canonical bytes.

Approach (locked by SDD-08 chunk contract + Constitution III/VIII/IX/X/XI;
not subject to research alternatives):

- **CanonicalJSON**: a single recursive encoder driven by `reflect.Value`
  that walks the input and emits sorted-key JSON at every depth. Map
  keys (`reflect.Map` with `reflect.String` key kind) are sorted
  lexicographically by raw key bytes. Struct fields are emitted in
  alphabetical order of the resolved field name (the `json:"name"` tag
  is honoured for renaming only — `omitempty` and other options are
  ignored so the output is byte-deterministic for any concrete struct
  value). The encoder NEVER invokes a user-defined `MarshalJSON` hook
  (the spec FR-022 anti-feature: a non-deterministic hook would silently
  break signature verification). Already-canonical bytes can be embedded
  verbatim via the `RawMessage` escape hatch (a `[]byte` named type the
  encoder special-cases). Non-finite floats (`math.IsNaN` /
  `math.IsInf`) and unsupported Go kinds (`reflect.Func`, `reflect.Chan`,
  `reflect.UnsafePointer`, non-string-keyed maps, complex numbers) are
  rejected with `ErrCanonicalUnsupported` and produce **no partial
  output**. Test-set covers both the `map[string]any` gotcha case
  (stdlib `encoding/json` sorts map keys at the top level only — the
  recursive encoder must sort at every depth) and the struct gotcha
  (stdlib emits struct fields in declaration order regardless of name —
  the recursive encoder must alphabetise).

- **Sign / Verify**: stdlib `crypto/ecdsa.SignASN1` and
  `crypto/ecdsa.VerifyASN1` over `sha256.Sum256(payload)`. The
  `*ecdsa.PrivateKey` / `*ecdsa.PublicKey` arguments are the same types
  the rest of the project uses (per SDD-01 `keys.DeriveClientKey`), so
  the integration cost across SDD-12 and SDD-16 is zero. The signature
  is DER-encoded. `Verify` returns `nil` on success and
  `ErrSignatureInvalid` for **every** failure mode (wrong key, tampered
  payload, malformed signature, wrong DER framing) — the recipient
  cannot distinguish "wrong key" from "tampered payload" by error type
  (FR-005), denying signature-shape leakage via timing or error
  identity. **No new crypto dependencies.** The chunk contract names
  go-bitcoin Bitcoin-message signing as the implementation; the plan
  honours the *intent* (ECDSA over SHA-256 of the canonical bytes,
  using the secp256k1 curve already in the project) while using stdlib
  primitives — see [research R-002](./research.md) for the substitution
  rationale (same pattern SDD-06's R-003 used for the deprecated
  `filepath.HasPrefix` substitute).

- **NonceCache**: `sync.Map[string]time.Time` (key is the nonce; value
  is the absolute expiry computed from the per-call TTL). `Add` is the
  full state-mutation surface and is concurrency-safe via three
  primitives: `sync.Map.LoadOrStore` for the fresh-insert path
  (FR-007 / FR-008 — exactly-one-winner semantics for first-seen),
  `sync.Map.CompareAndSwap` for the rare expired-but-not-yet-swept
  reuse path (lets a recurring-nonce-after-TTL race produce
  exactly-one winner without a lock), and `sync.Map.CompareAndDelete`
  for the sweep. `Run(ctx)` is **synchronous** — the caller is
  responsible for invoking it in their own goroutine
  (`go cache.Run(ctx)` per Constitution IX "every goroutine has a
  clear owner"); Run blocks on a ticker, sweeps every 30 s, and
  returns when `ctx.Done()` fires after writing a single
  "stopped" log line. The cache spawns **zero** goroutines
  outside Run — `New`-only construction never runs concurrent code,
  and `Add` is fully synchronous. The cache MUST NOT impose a
  capacity cap (FR-020) — DoS resistance is the consumer's
  responsibility (Tailscale boundary + server-side rate limiting).

- **IsFreshTimestamp**: a pure function that returns
  `|now() - ts| <= skew`. The clock is a package-private function
  variable `nowFn func() time.Time` defaulting to `time.Now`; tests
  swap it via a test-only helper. The function returns `bool`; the
  consumer maps `false` to `ErrTimestampStale` (the wrapping happens
  at the verification entry point, not inside this primitive — the
  primitive itself does not know whether the timestamp came from a
  signed request or a clock-sync probe).

- **Replay-defence ordering** (consumer concern, but documented and
  tested here): a verification path that combines all three primitives
  MUST run signature verification FIRST, then timestamp freshness,
  then nonce-cache `Add`. This ordering implements FR-013 + FR-014:
  an attacker MUST NOT be able to "burn" a nonce for an honest client
  by submitting a forged signature or a stale timestamp. The package
  does NOT compose these primitives into a single `VerifyRequest`
  function (the consumer is the right place — the server reads
  registered-client keys from its registry, the client signs with its
  own key); but the [quickstart](./quickstart.md) shows the canonical
  ordering and the [contract](./contracts/api.md#composition-recipe)
  enshrines it.

- **Logging**: the package uses `slog.Default()` only inside Run for
  the single "stopped" log line. **NEVER** logs a nonce, a signature
  byte, a payload byte, or a private-key value (FR-017). Diagnostic
  surfaces identify failure category and field, never content.

- **Fuzz target `FuzzVerifyRequest`** feeds random `(payload, sig)`
  byte streams to `Verify` against a fixed (test-derived) public key.
  The fuzz contract: no panic, no unbounded allocation, every error
  returned is `ErrSignatureInvalid` (or `ctx.Err()` for the
  cancelled-context path). The 60 s gate is enforced by the
  implement-phase release-step list; the seed corpus seeds the four
  obvious shapes (empty payload, empty sig, valid-shape-wrong-key,
  truncated-DER).

Exported API (locked at SDD-08; mirrored into `docs/PACKAGE-MAP.md` once
the implement commit lands):

```go
// Package github.com/mrz1836/hush/internal/transport/sign

func CanonicalJSON(v any) ([]byte, error)
func Sign(ctx context.Context, key *ecdsa.PrivateKey, payload []byte) ([]byte, error)
func Verify(ctx context.Context, key *ecdsa.PublicKey, payload, sig []byte) error

type NonceCache interface {
    Add(ctx context.Context, nonce string, ttl time.Duration) (firstSeen bool, err error)
    Run(ctx context.Context)
}
func NewNonceCache() NonceCache

func IsFreshTimestamp(ts time.Time, skew time.Duration) bool

// Already-canonical-bytes escape hatch (per FR-022).
type RawMessage []byte

// Sentinel errors — full catalogue and wrap-relationships in contracts/api.md.
var ErrSignatureInvalid, ErrNonceReplay, ErrTimestampStale,
    ErrCanonicalUnsupported, ErrNonceEncoding, ErrNonceTTLInvalid
```

The chunk contract names six sentinels (the first three plus the
`ErrSignatureInvalid` umbrella implied by FR-005); the catalogue above
is the spec-derived completion of the ellipsis. The three additions
(`ErrCanonicalUnsupported`, `ErrNonceEncoding`, `ErrNonceTTLInvalid`)
each implement a distinct FR (FR-002/FR-023, FR-021, FR-012) that the
chunk contract treats as separate categories; they are NOT
collapsible into the named four without violating spec FR-015's
"distinct, named" guarantee.

## Technical Context

**Language/Version**: Go 1.26.1 (per `go.mod`); `CGO_ENABLED=0`
(Constitution IX).

**Primary Dependencies**:
- Go stdlib: `bytes`, `context`, `crypto/ecdsa`, `crypto/rand`,
  `crypto/sha256`, `encoding/json`, `errors`, `fmt`, `log/slog`,
  `math`, `reflect`, `sort`, `strconv`, `sync`, `time`.
- Intra-repo: NONE at runtime. The package is a leaf primitives
  library — it accepts `*ecdsa.PrivateKey` / `*ecdsa.PublicKey` from
  the caller, returns bytes / errors, and imports nothing from
  `internal/keys`, `internal/vault`, `internal/config`, or
  `internal/logging`. Test files MAY import `internal/keys` to
  generate fixture keys (cheap, deterministic).
- **Zero new external dependencies.** The chunk contract's
  `github.com/bitcoinschema/go-bitcoin` reference is honoured at the
  *intent* level (ECDSA over SHA-256 of canonical bytes on the
  secp256k1 curve) using stdlib `crypto/ecdsa` — the secp256k1 curve
  is already supplied by the existing `decred/dcrd/dcrec/secp256k1/v4`
  module via the `*ecdsa.PrivateKey` returned by SDD-01's
  `DeriveClientKey`. See research R-002 for the substitution
  rationale and the security equivalence argument.

**Storage**: stateful in-process only — the `NonceCache` holds
nonce → expiry pairs in a `sync.Map`. No disk I/O. No network I/O. No
process-wide state outside the per-cache instance. A server restart
resets the nonce set; replays that span a restart are caught by the
timestamp freshness check (per `docs/SECURITY.md` Layer 4 ±30 s skew).

**Testing**: `go test ./internal/transport/sign/...` (table-driven unit
tests per `.github/tech-conventions/testing-standards.md`); `magex
test:race` race-clean (the concurrent-Add test is the load-bearing
race-detector exercise); `go test -fuzz=FuzzVerifyRequest -fuzztime=60s
./internal/transport/sign/` with no panics and no new corpus rows
representing crashes. Coverage measured via `go test -cover
./internal/transport/sign/`; target **100%** per Constitution VIII
Critical band (request signing is named explicitly).

**Target Platform**: macOS (darwin amd64/arm64) and Linux server
(amd64/arm64) per `.goreleaser.yml`. Windows is out of scope. The
package has zero platform-conditional code paths; the same source
compiles and passes on every supported `GOOS/GOARCH`.

**Project Type**: Single Go module (`github.com/mrz1836/hush`) with a
flat `internal/<domain>` layout per `docs/PACKAGE-MAP.md`.
`internal/transport/sign` is a new package under the existing
`internal/transport/` placeholder directory; SDD-09 will land
`internal/transport/ecies` as a sibling package in a later chunk.

**Performance Goals**:
- `CanonicalJSON` total wall time: ≤100 µs for a typical payload
  (≤4 KiB, ≤20 fields, ≤3 nesting depth).
- `Sign`: ≤2 ms per call on a 2026-class CPU (dominated by ECDSA
  scalar multiplication on secp256k1, generic-curve path).
- `Verify`: ≤2 ms per call (same scalar multiplication cost).
- `NonceCache.Add`: O(1) — single `sync.Map.LoadOrStore`. Sub-µs.
- Sweep goroutine: 30 s tick interval; per-sweep work is O(N) over
  cache entries with `CompareAndDelete`, ≤1 ms for a 10 k-entry
  cache.
- `IsFreshTimestamp`: O(1) — two `time.Time` comparisons. Sub-100 ns.
- `FuzzVerifyRequest`: ≥1 k iter/s on a CI runner; the 60 s gate
  exercises ≥60 k randomly-generated `(payload, sig)` pairs.

**Constraints**:
- **100% test coverage** on `internal/transport/sign/` — Constitution
  VIII Critical band ("Vault crypto, key derivation, JWT, ECIES,
  request signing"). Every documented sentinel + every locked function
  + every edge case in spec §Edge Cases has at least one named test.
- **Fuzz `FuzzVerifyRequest` runs ≥60 s clean** (no panic, no
  unbounded allocation, every error a typed sentinel) per the chunk
  contract and Constitution VIII fuzz target #4 (request signature
  payload).
- **Zero panics on hostile input.** Every code path that can fail
  returns a typed sentinel error. Random byte streams (any length,
  any content) MUST NOT crash `Verify`, `CanonicalJSON`, or
  `NonceCache.Add`.
- **No `init()` function**, no mutable package-level globals beyond
  the read-only sentinel-class exported `var Err...` declarations and
  the package-private `nowFn` clock variable (set-once at package
  load; tests swap it via a test-only helper that captures and
  restores). The `nowFn` swap is gated by a `_test.go`-only helper to
  prevent production code from mutating the clock — same constitutional
  class as the test-only `RedactPatterns` reset in `internal/logging`
  (SDD-05).
- **No goroutines outside `Run`.** `New` does not spawn. `Add` does
  not spawn. `Sign`, `Verify`, `CanonicalJSON`, `IsFreshTimestamp`
  are pure / synchronous. Only `Run` is a goroutine target — and it
  is one-shot, ticker-driven, and ctx-cancellable.
- **No metrics emission.** The package is a leaf primitives library
  per FR-024; consumers derive operational counters from the
  identity of returned sentinel errors.
- **No nonce / signature / payload bytes in any log line or error
  message** (FR-017). Tests assert this against
  `slog.NewJSONHandler(buf)` captures.
- The `Server` / `*ecdsa.PrivateKey` / `*ecdsa.PublicKey` lifecycle
  is the consumer's responsibility — this package never holds a
  secret value of its own.
- No CGO, no `vendor/`, no `init()`, **one** goroutine target
  (`Run`).

**Scale/Scope**:
- Five production source files: `canonical.go` (CanonicalJSON +
  RawMessage type + reflect-walker), `sign.go` (Sign), `verify.go`
  (Verify), `nonce.go` (NonceCache interface + nonceCache struct +
  Add + Run + sweep), `timestamp.go` (IsFreshTimestamp + nowFn
  clock indirection). One additional declarative file: `errors.go`
  (sentinel error declarations), split out for grep-locality with
  the SDD-06 / SDD-03 / SDD-02 / SDD-01 precedent. One package-doc
  file: `doc.go` (package comment + Constitution citations).
- Six test files: `canonical_test.go` (10+ shape variants + NaN/Inf
  rejection + unsupported-type rejection + RawMessage embedding),
  `sign_test.go` (round-trip + wrong-key + DER-shape edge),
  `verify_test.go` (sentinel-identity + panic-free + ctx-cancel),
  `nonce_test.go` (first-seen / replay / TTL-elapsed / concurrent
  /  Run-lifecycle / sweep), `timestamp_test.go` (fresh / too-old
  / future-skew / boundary-determinism), `verify_fuzz_test.go`
  (FuzzVerifyRequest + 4-file seed corpus).
- Estimated ~700 LOC of production Go (canonical encoder is the
  largest file at ~250 LOC) and ~1100 LOC of tests.
- Exported surface: 6 functions (`CanonicalJSON`, `Sign`, `Verify`,
  `NewNonceCache`, `IsFreshTimestamp`, plus `NonceCache.Add` /
  `NonceCache.Run` as interface methods), 1 type (`NonceCache`
  interface), 1 named-byte-slice type (`RawMessage`), 6 sentinel
  errors. Total exported identifiers: 14.

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

### Principles in scope (per SDD-08)

| Principle | Constraint | Plan compliance |
|-----------|------------|-----------------|
| **III. Defense in Depth — Layer 4 (request signing + replay protection)** | Every `/claim` and `/revoke` MUST be ECDSA-signed by the client over a canonical-JSON payload (alphabetical keys, compact form, SHA-256 hash). Server verifies signature, IP allowlist, nonce uniqueness within 60 s, and timestamp freshness ±30 s. The package backs the canonical-JSON, signature, nonce, and timestamp halves of that contract — IP allowlist is SDD-12's job. | Sign/Verify use stdlib ECDSA over `sha256.Sum256(CanonicalJSON(payload))`. NonceCache implements the 60 s window via per-call TTL (consumer-supplied; `docs/SECURITY.md` Layer 4 default = 60 s, applied in SDD-12). IsFreshTimestamp implements the ±30 s skew via a per-call `skew time.Duration` argument (consumer-supplied; default `30s` from SDD-06's `DefaultClockSkew`). Each replay-defence failure is a distinct sentinel: `ErrSignatureInvalid`, `ErrNonceReplay`, `ErrTimestampStale` (per FR-015 + spec User Story 6). The composition recipe in [contracts/api.md](./contracts/api.md#composition-recipe) enshrines the FR-013 + FR-014 ordering (signature → timestamp → nonce-Add) so an attacker cannot "burn" a nonce by submitting a forged signature or a stale timestamp. ✅ |
| **VIII. Testing Discipline — TDD + 100% coverage + fuzz target #4** | Test-first; **100% coverage** for security-critical packages (request signing is named in the Critical band); fuzz target #4 (request signature payload parsing) ≥60 s clean in CI. Every spec FR + every spec SC has at least one named test. | The /speckit-tasks-phase prompt (chunk contract Prompt 4) enforces test-first ordering: every behaviour-contract task has a paired test-writing task scheduled BEFORE it. Coverage gate is `go test -cover ./internal/transport/sign/` ≥100% in the implement-phase release-step list. Fuzz target `FuzzVerifyRequest` is constitutional fuzz target #4 ("Request signature payload parsing"); the chunk contract names the 60 s gate. The named tests (TestCanonical_SortsAtAllDepths over 10 known shapes, TestCanonical_RejectsNaN/Inf, TestCanonical_StructAndMap, TestSign_VerifyRoundTrip, TestVerify_WrongKeyFails, TestNonce_Add{New,Duplicate,ExpiredAfterSweep}, TestNonceCache_ConcurrentAdd, TestTimestamp_{FreshAccepted,TooOldRejected,FutureSkewRejected}) are the floor; `tasks.md` will expand to one test per documented sentinel (~6) + one test per spec User Story acceptance scenario (≥18) + the fuzz target. ✅ |
| **IX. Idiomatic Go Discipline — context first, no init, explicit goroutine lifecycle** | `context.Context` is the first parameter of every function that does cancellable work. No `init()`. No mutable package-level globals beyond the sentinel-class `var Err...` declarations. No fire-and-forget goroutines: every goroutine has a clear owner, an explicit cancellation path, and a documented termination condition. Errors wrapped with `%w`. Compare with `errors.Is`. | `Sign`, `Verify`, and `NonceCache.Add` accept `ctx context.Context` as first parameter. `CanonicalJSON` and `IsFreshTimestamp` are pure / synchronous and do not — they have no I/O and no cancellation surface. **Zero `init()` functions.** The package's only package-level `var`s are: (a) the sentinel error declarations, set-once at package load, never mutated — same constitutional class as `internal/keys`, `internal/vault`, `internal/config` (SDD-01/-03/-06); (b) the `nowFn func() time.Time` clock indirection, declared `//nolint:gochecknoglobals` with a `_test.go`-only swap helper (`setClockForTest`) that captures and restores the original — same pattern as test-only fixture seams elsewhere in the project. **One goroutine target** (`Run`): the caller invokes `go cache.Run(ctx)`; Run blocks on a ticker; ctx-cancellation triggers ticker stop, sweep-loop exit, and a single "stopped" `slog.Info` line; no other goroutine is spawned anywhere in the package. All errors wrap underlying causes via `%w` where wrapping is meaningful (e.g., `Verify` wraps stdlib's `ecdsa.VerifyASN1` failure as `fmt.Errorf("hush/transport/sign: verify: %w", ErrSignatureInvalid)`). All comparisons via `errors.Is`. ✅ |
| **X. Observability & Redaction — no nonces, signatures, payloads, or keys in logs** | A secrets-broker that logs the secrets it brokers is worse than no logging at all. Diagnostic surfaces identify failure category, never content. Type-driven redaction is mandatory for any value carrying secret material. | The package emits exactly **one** log line — the "nonce cache sweep stopped" line on Run exit — and that line carries `reason=ctx.Err().Error()` only (no nonce, no count of swept entries, no per-key data). Errors returned from `Sign` / `Verify` / `Add` carry the failure category in the sentinel identity; the wrapping message names the *operation* (`verify`, `sign`, `nonce-cache add`) but never the payload bytes, the signature bytes, the nonce, or the key. A fuzz-pressure test (`TestVerify_NoLeakOnFuzzInput`) captures `slog.NewJSONHandler` output across 1000 random failed verifications and asserts the nonce / signature / payload bytes never appear. **No private-key value is ever logged** — `Sign` and `Verify` log nothing at all; `Add` logs nothing. Only `Run` logs (one line, no key material). ✅ |
| **XI. Native-First, Minimal Dependencies** | Stdlib first. Every NEW direct dependency requires a written justification per the trusted-sources hierarchy. The crypto stack pinned in Principle III (BIP32, secp256k1, ECIES) is the ONLY cryptographic dependency surface; adding another crypto library requires a constitutional amendment. | **Zero new direct dependencies.** The chunk contract's `github.com/bitcoinschema/go-bitcoin` reference is honoured at the *intent* level: the package implements ECDSA over SHA-256 of the canonical bytes on the secp256k1 curve — the same algorithmic and curve choices the chunk contract names — using **stdlib** `crypto/ecdsa.SignASN1` / `VerifyASN1` and `crypto/sha256`. The secp256k1 curve is already supplied by the project's existing `decred/dcrd/dcrec/secp256k1/v4` direct dependency (introduced in SDD-01); SDD-08 adds nothing new to `go.mod`. This is a stdlib-correct refinement of the chunk contract's named library, in the same spirit as SDD-06's R-003 substitution of deprecated `filepath.HasPrefix` with `filepath.Rel`-based containment — the spec's behavioural contract (sign/verify deterministically over a canonical payload) is satisfied identically; only the implementation library differs. See research [R-002](./research.md#r-002--ecdsa-signverify-stdlib-substitution) for the security-equivalence argument and the alternatives considered. ✅ |

### Other principles (not in scope but checked for non-violation)

- **I (Zero Files at Rest on Agent Machines):** out of scope — this
  package has zero filesystem surface. ✅
- **II (Approval is Human):** out of scope — no approval surface. ✅
- **IV (Supervisor for Daemons):** out of scope — supervisor uses
  this package as a primitives consumer (SDD-19); SDD-08 itself
  defines no daemon lifecycle. ✅
- **V (Staleness is Visible):** out of scope. ✅
- **VI (Tailscale-Only):** out of scope at the package level — the
  package validates signatures and nonces irrespective of network;
  the Tailscale boundary is enforced by SDD-06 (config) + SDD-12
  (server middleware). ✅
- **VII (CLI Design Standards):** out of scope — this package
  defines no CLI surface. ✅

### Gate result

**PASS** — every principle in scope is satisfied. **Zero Complexity
Tracking entries** (the SDD-08 implementation introduces no new
dependencies and no constitutional deviations). The Constitution Check
is re-evaluated post-design (after Phase 1) below.

## Project Structure

### Documentation (this feature)

```text
specs/008-transport-sign/
├── plan.md                  # This file (/speckit-plan command output)
├── research.md              # Phase 0 output — locked HOW decisions
├── data-model.md            # Phase 1 output — types + nonce-cache lifecycle + sentinel catalogue
├── quickstart.md            # Phase 1 output — consumer integration recipe (SDD-12 + SDD-16)
├── contracts/
│   └── api.md               # Phase 1 output — exported API contract (locks PACKAGE-MAP §internal/transport)
├── checklists/              # Pre-existing (untouched by /speckit-plan)
├── spec.md                  # WHAT contract (already written by /speckit-specify + /speckit-clarify)
└── tasks.md                 # Phase 2 output (/speckit-tasks command — NOT created by /speckit-plan)
```

### Source Code (repository root)

```text
internal/transport/sign/
├── doc.go                   # Package doc: Constitution III/VIII/IX/X/XI citations + Layer-4 roster
├── canonical.go             # CanonicalJSON + RawMessage + recursive reflect-walker
├── sign.go                  # Sign (stdlib ecdsa.SignASN1 over sha256.Sum256(payload))
├── verify.go                # Verify (stdlib ecdsa.VerifyASN1 mirror)
├── nonce.go                 # NonceCache interface + nonceCache struct + Add + Run + sweep
├── timestamp.go             # IsFreshTimestamp + nowFn clock indirection
├── errors.go                # Sentinel error declarations + wrap relationships
├── canonical_test.go        # 10+ shape variants + NaN/Inf + unsupported-type + RawMessage
├── sign_test.go             # Round-trip + wrong-key + DER-shape + ctx-cancel
├── verify_test.go           # Sentinel-identity + panic-free + ctx-cancel + log-redaction
├── nonce_test.go            # FirstSeen / Replay / ExpiredAfterSweep / Run-lifecycle / Concurrent
├── timestamp_test.go        # Fresh / too-old / future-skew / boundary determinism
├── verify_fuzz_test.go      # FuzzVerifyRequest + seed corpus loader
└── testdata/
    └── fuzz/
        └── FuzzVerifyRequest/
            ├── empty-payload-empty-sig
            ├── valid-shape-wrong-key
            ├── truncated-der
            └── garbage-bytes

go.mod                       # NO CHANGES — zero new direct dependencies
go.sum                       # NO CHANGES
```

**Structure Decision**: hush is a single Go module
(`github.com/mrz1836/hush`) with a flat `internal/<domain>` layout
defined in `docs/PACKAGE-MAP.md`. SDD-08 fills a new sub-package
`internal/transport/sign/` under the existing `internal/transport/`
placeholder directory (currently `.gitkeep`-only). The package ships
seven production source files; the chunk contract's "Files:" list named
five (`canonical.go`, `sign.go`, `verify.go`, `nonce.go`,
`timestamp.go`). The plan adds two: `errors.go` (sentinel error
declarations colocated for grep-locality, the same locality refinement
SDD-06 made under the same constitutional reading) and `doc.go`
(package-level doc comment with Constitution citations). The
chunk-contract's file list is read as the **minimum** set: every file
the contract names is present, and the package may add purely
declarative files where idiomatic Go discipline calls for them. No
production logic is added beyond what the chunk contract describes.

The package import path is `github.com/mrz1836/hush/internal/transport/sign`.
Per `docs/PACKAGE-MAP.md` §`internal/transport/`, this is a leaf
primitives library: the allowed dependency direction is `internal/server
→ internal/transport/sign` (SDD-12 will consume the Verify path) and
`internal/cli → internal/transport/sign` (SDD-16 will consume the
Sign path); SDD-08 itself imports nothing intra-repo at runtime.
SDD-09 (`internal/transport/ecies`) will land as a sibling sub-package
in a later chunk and may share helper utilities with SDD-08 if a
genuine shared concern emerges; that is a future-chunk concern.

The `testdata/fuzz/FuzzVerifyRequest/` directory ships four seed
files matching the spec edge-cases (empty-payload-empty-sig,
valid-shape-wrong-key, truncated-DER, garbage-bytes) so the 60 s CI
fuzz gate exercises the whole `Verify` surface from the first run
rather than spending most of its budget bouncing off the
"is-this-DER" early-out.

## Post-Design Constitution Re-check

Re-evaluated after Phase 1 design artifacts (`research.md`,
`data-model.md`, `contracts/api.md`, `quickstart.md`) were drafted:

| Principle | Phase 1 introduced | Re-check |
|-----------|--------------------|----------|
| **III** | The composition recipe (signature → timestamp → nonce-Add) is enshrined in [contracts/api.md](./contracts/api.md#composition-recipe). The recipe is the FR-013 + FR-014 ordering: an attacker cannot "burn" a nonce by submitting a forged signature or a stale timestamp. SDD-12's verify middleware MUST follow this ordering; the contract makes the ordering checkable in code review. | PASS — the layer-4 contract is enforced by ordering, not by an additional guard inside the primitives. The primitives are usable in any order if a future consumer needs to (rare); the canonical ordering is documented and tested via a recipe-validating integration test scheduled for SDD-12. |
| **VIII** | The contract enumerates 30+ named tests across the six test files, including the 13 floor tests from the chunk contract plus one test per spec User Story acceptance scenario plus four fuzz seed entries. Coverage gate is 100% per the Critical band. | PASS — every spec FR and every spec SC has at least one named test; the fuzz target ships with a deterministic 4-file seed corpus so CI's first run is meaningful. |
| **IX** | Phase 1 confirmed: zero `init()`, zero mutable globals beyond the documented sentinel-class `var Err...` and the test-swappable `nowFn`, all errors wrapped with `%w`, all comparisons via `errors.Is`. The Run goroutine is the package's single long-running goroutine; its termination condition (`ctx.Done()`), its log line on exit ("stopped"), and its zero-on-construction guarantee are all part of the locked contract. | PASS — no new violations introduced. |
| **X** | The error catalogue is finalised. Every sentinel's identity is the failure category; no error message embeds payload, nonce, signature, or key bytes. The Run sweep log line carries `reason=ctx.Err()` only. The `TestVerify_NoLeakOnFuzzInput` test scaffolding in `verify_test.go` is in the test floor. | PASS — diagnostic surfaces audited and clean. |
| **XI** | Zero new direct dependencies; the `go.mod` diff for the implement commit is empty. The chunk contract's named library (go-bitcoin) is substituted for stdlib + an existing project dep (decred secp256k1 via `*ecdsa.PrivateKey`); the substitution is documented in research R-002 and re-asserted in the implement-phase PR description. | PASS — no Complexity Tracking entry needed (the substitution is constitutionally-aligned, not a deviation). |

**Final result**: PASS. **No Complexity Tracking entries.** No new
violations introduced by the design phase; no new dependencies
introduced; the Layer-4 contract is enforced via ordering documented
in the contract and tested by integration tests scheduled for SDD-12.

## Complexity Tracking

> Fill ONLY if Constitution Check has violations that must be justified.

*(empty — no constitutional deviations)*
