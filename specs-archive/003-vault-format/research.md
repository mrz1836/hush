# Phase 0 Research — SDD-03 (`internal/vault`)

This document records the locked decisions for the `internal/vault`
package. Every "HOW" question of consequence is already answered by
the SDD-03 chunk contract, the spec's Clarifications session, the
constitution's Principles III/VIII/X/XI, and the locked SDD-02
exported surface. Nothing here required a "research agent" round —
the role of this document is to **make each locked choice explicit**
along with the alternatives considered and rejected, so that a future
reader can verify that no NEEDS CLARIFICATION marker remains and that
no choice was made without an audit trail.

There are no `[NEEDS CLARIFICATION]` markers in `spec.md`. Every
clarification raised by `/speckit-clarify` was resolved into an FR
in the spec's "Clarifications → Session 2026-04-27" block before
this plan was drafted.

---

## Decision 1 — Authenticated-encryption algorithm and primitive source

**Decision:** AES-256-GCM via `crypto/aes` + `crypto/cipher.NewGCM`
(Go stdlib).

**Rationale:**
- `docs/SPEC.md` FR-2 names AES-256-GCM as the at-rest authenticated-
  encryption algorithm; the SDD-03 chunk contract restates this as a
  MUST.
- Constitution XI bans new crypto dependencies; the stdlib
  implementation is already on the allowed surface.
- AES-256-GCM provides 256-bit confidentiality + a 128-bit
  authentication tag in one primitive; the failure-mode classification
  in FR-021 + FR-028 ("authentication failed") maps cleanly to the
  single error returned by `gcm.Open` on tag mismatch *or* ciphertext
  truncation below the tag length, which is exactly the spec's
  intent.
- The stdlib AEAD interface (`cipher.AEAD.Open`) returns a single
  generic error on any verification failure, with a deterministic
  zero-byte plaintext on failure — the package wraps it once with
  `%w` and returns `ErrAuthFailed`, so the caller sees the typed
  sentinel and `errors.Is(err, ErrAuthFailed)` works.

**Alternatives considered:**
- **ChaCha20-Poly1305 (`golang.org/x/crypto/chacha20poly1305`):**
  rejected. AES-256-GCM is the project's locked primitive (FR-2);
  switching would require a constitutional crypto-dep amendment AND
  a SPEC change. Both refused by SDD-03's chunk contract.
- **NaCl secretbox (`golang.org/x/crypto/nacl/secretbox`):** rejected
  for the same reasons; also adds a non-stdlib direct dep.
- **AES-256-CTR + HMAC-SHA-256 (encrypt-then-MAC) hand-rolled:**
  rejected — more code surface, more places to make a tag-comparison
  mistake, no security benefit.

---

## Decision 2 — Salt and nonce sizes; randomness source

**Decision:** 16-byte salt + 12-byte AES-GCM nonce, both filled with
`crypto/rand.Read` on every `Save`. `math/rand` is not imported.

**Rationale:**
- 12-byte nonce is the AES-GCM standard recommended by NIST SP
  800-38D; matches `cipher.AEAD.NonceSize()` for the GCM mode the
  stdlib returns.
- 16 bytes of salt is consistent with the SDD-01 KDF (Argon2id
  consumes a 16-byte salt) — the salt field is *carried* by this
  package on behalf of SDD-01's re-derivation, not interpreted here.
- Constitution III: "cryptographic operations MUST use `crypto/rand`
  for entropy — never `math/rand`". Applied without exception.
- Fresh nonce on every `Save`: AES-GCM's confidentiality guarantee
  collapses if a (key, nonce) pair is reused for two distinct
  plaintexts. FR-016 names this explicitly. The package therefore
  generates a fresh 12 bytes on every save call, with no caching, no
  derivation, no monotonic counter.

**Alternatives considered:**
- **Deterministic nonce derived from a counter or from the
  plaintext hash:** rejected. Unnecessary complexity; deterministic
  AEAD modes (e.g. AES-GCM-SIV) are not in the project's allowed
  crypto surface, and a counter would be brittle across save calls.
- **Larger 24-byte XSalsa20-style nonce:** moot — we are not using
  XSalsa20.

---

## Decision 3 — Plaintext payload encoding (JSON, not msgpack/CBOR/protobuf)

**Decision:** JSON array of objects with three string-typed fields
on the wire — `name`, `description`, `value` (base64-encoded). The
SDD-03 chunk contract names JSON as the locked encoding.

**Rationale:**
- Stdlib `encoding/json` requires no new dependency (Constitution XI
  satisfied).
- JSON is human-inspectable when the plaintext is decrypted in a
  controlled debugging context — useful for the operator-facing
  failure modes (`ErrInvalidName`, `ErrDuplicateName`).
- The encoding is contained inside the AEAD envelope; the wire-shape
  has no public consumer beyond this package and its tests, so the
  argument "JSON is verbose" is operationally irrelevant — even at
  500 secrets × 8 KiB values, the plaintext stays comfortably below
  the 64 MiB cap (FR-019a).

**Alternatives considered:**
- **CBOR (`github.com/fxamacker/cbor`):** rejected. New direct dep.
  Constitution XI requires written justification per dep; the JSON
  alternative is sufficient.
- **MessagePack:** rejected for the same reason.
- **Protocol Buffers:** rejected — adds tooling burden (`protoc`),
  generated-code drift, and a new direct dep.
- **A hand-rolled length-prefixed binary format:** rejected — adds a
  new parser surface (= a new fuzz target) without security or size
  benefit.

---

## Decision 4 — Custom `UnmarshalJSON` on the value-wrapper type

**Decision:** Define an internal wrapper type
`type wireValue struct{ sb *securebytes.SecureBytes }` whose
`UnmarshalJSON([]byte)` (a) verifies the JSON token is a quoted
string, (b) base64-decodes the string body directly into a freshly
allocated `[]byte`, (c) calls `securebytes.New(buf)` (which copies
into mlocked memory and zeroes the transient input slice), (d)
stores the resulting `*SecureBytes` pointer.
The corresponding `MarshalJSON` borrows the bytes via `Use(fn)`,
base64-encodes them into a JSON-quoted string in one pass, and
returns the byte slice.

**Rationale:**
- Spec FR-009 / FR-022: secret values MUST never be materialised as
  Go `string`. The default reflection-based JSON path would
  unmarshal the quoted base64 string into a `string` field first;
  this is exactly the failure mode the FR forbids. A custom
  `UnmarshalJSON` is the *only* idiomatic Go way to bypass that
  string allocation.
- The wrapper pattern keeps `Secret` (the exported type) ergonomic
  for callers (`Value *securebytes.SecureBytes`) while letting the
  on-wire shape carry a base64 string. The wrapper is package-
  private; consumers never see it.
- The constructor of `*SecureBytes` is responsible for zeroing the
  transient `[]byte` post-copy (SDD-02 contract), so the only
  remaining hot bytes after `UnmarshalJSON` returns are inside the
  mlocked buffer.

**Alternatives considered:**
- **Decode into a `[]byte` field directly (no wrapper):** rejected.
  `encoding/json`'s default `[]byte` handling base64-decodes the
  quoted string into a `[]byte` — but the resulting `[]byte` lives
  on the regular Go heap and is GC-managed, never zeroed, never
  mlocked. We would then have to copy it into `*SecureBytes` and
  zero the source — which is exactly what the wrapper's
  `UnmarshalJSON` does, just without the mistake of leaving the
  intermediate `[]byte` reachable inside the JSON decoder's struct
  field after the call.
- **Hand-roll a JSON tokenizer:** rejected — over-engineering;
  `json.Unmarshal` is already the right shape.

---

## Decision 5 — Atomic-write strategy: temp-file + fsync + rename in same directory

**Decision:** Write to `<path>.tmp` in the SAME directory as
`<path>`, `f.Sync()`, `f.Close()`, `os.Chmod(<path>.tmp, 0600)`,
`os.Rename(<path>.tmp, <path>)`, `os.Chmod(<path>, 0600)`. On any
mid-flight failure (seal failure, write failure, sync failure,
chmod failure), best-effort `os.Remove(<path>.tmp)` and surface
the original error.

**Rationale:**
- POSIX `rename(2)` is atomic with respect to readers when source
  and destination are on the same filesystem; placing the temp file
  in the same directory guarantees same-FS placement on every
  supported platform.
- `f.Sync()` before rename ensures the new ciphertext bytes are
  durable before the directory entry is swapped — eliminates the
  failure mode where a power loss between rename and the
  filesystem's lazy data flush leaves a "renamed but empty" file.
- `os.Chmod(0600)` on the temp file *before* rename neutralises the
  process's umask without race; the post-rename `os.Chmod` is
  belt-and-braces in case the rename swapped in a file the umask
  had already touched.
- Spec FR-013 + Spec Clarifications Q4: best-effort tmp cleanup on
  every controlled error path; the cleanup error is logged at debug
  level and never masks the original error. The SIGKILL case
  (process death between tmp-create and rename) is upstream's
  problem (the rotation-CLI sweep).

**Alternatives considered:**
- **Write directly to `<path>` with `O_TRUNC`:** rejected — a
  reader can observe a zero-length file mid-write; this is the
  exact failure mode AC-2 forbids.
- **Write to `/tmp` then rename:** rejected — `/tmp` is typically
  on a different filesystem; `os.Rename` would either fail with
  `EXDEV` or fall back to copy+delete (Go's stdlib `os.Rename`
  fails with `EXDEV`, it does not silently copy), losing atomicity.
- **`renameat2(RENAME_EXCHANGE)`:** Linux-only, not in the
  supported-platform intersection.

---

## Decision 6 — File and parent directory mode policy (exact equality, not "at most")

**Decision:** `Save` and `Load` enforce file mode `== 0600` and
parent directory mode `== 0700`, both via exact equality on
`info.Mode().Perm()`. Any deviation — laxer **or** stricter —
produces `ErrFilePermsLoose`.

**Rationale:**
- The SDD-03 chunk contract phrases the rule as "refuse if mode
  `!= 0600` OR parent mode `!= 0700`" — i.e. exact equality.
- Constitution Security Requirements: "Vault: 0600. Dirs: 0750/
  0700." — the rule is the exact mode, not "at most".
- A stricter mode (e.g. `0400`) would be operationally surprising
  but is not a security risk per se; nonetheless treating it as a
  failure surfaces a misconfiguration rather than silently
  accepting it.
- Spec FR-014 + FR-015 + FR-018 + FR-019 all use the exact-equality
  framing.

**Alternatives considered:**
- **"At most 0600 for the file, at most 0700 for the parent" (i.e.
  reject only laxer modes):** rejected for chunk-contract
  consistency; the equality check is simpler and stricter, and a
  misconfigured-stricter file is a real-world signal worth
  surfacing.

---

## Decision 7 — `Store.Get` returns a fresh `*SecureBytes` per call (not a borrow)

**Decision:** `Get(name)` looks up the internal container, copies
its payload into a fresh `[]byte` via `Use(fn)`, then constructs a
NEW `*SecureBytes` via `securebytes.New(buf)` (which copies into a
new mlocked buffer and zeroes the transient slice). The returned
container is owned by the caller; destroying it has no observable
effect on the store's internal copy.

**Rationale:**
- Spec FR-023 + SC-011: each consumer must own its returned
  container's lifecycle; destruction by one consumer must not
  affect any other consumer or any subsequent retrieval of the
  same name.
- A "borrow" return (handing back the same pointer) would couple
  every consumer's lifecycle and would make the SIGHUP-reload
  swap (SDD-10) much harder — the new vault's store must replace
  the old one and the old one must `Destroy()` cleanly without
  zeroing live consumers' buffers.
- Constitution III + X: every secret transit MUST sit inside a
  `*SecureBytes`; a `[]byte` return type (which would also avoid
  shared lifetime) is forbidden by the no-`string`-no-bare-`[]byte`
  redaction rule.

**Alternatives considered:**
- **Return the same `*SecureBytes` (borrow):** rejected — couples
  lifecycles; one consumer's `Destroy()` zeroes everyone else's
  view (Spec User Story 2 explicitly forbids this).
- **Return a `[]byte`:** rejected — bypasses Layer 5; violates
  Constitution X.

---

## Decision 8 — `Store` lifecycle: explicit `Destroy()` + `ErrStoreDestroyed`

**Decision:** `Store.Destroy()` zeroes every internally-held
`*SecureBytes` and sets a `destroyed` flag (idempotent).
Post-`Destroy` calls to `Get(name)` return the new sentinel
`ErrStoreDestroyed`, which is programmatically distinguishable from
`ErrSecretNotFound`.

**Rationale:**
- Spec Clarifications Q1: rejected the option that re-uses
  `ErrSecretNotFound` for post-destroy `Get`. The operator-facing
  remediation differs ("re-add a missing secret" vs "the store has
  already been torn down"), and Constitution V (loud failure)
  demands they be distinguishable.
- Idempotent `Destroy` matches the SDD-02 `*SecureBytes.Destroy`
  contract and lets callers sequence "destroy old vault, swap in
  new vault" without double-destroy panics.

**Alternatives considered:**
- **Re-use `ErrSecretNotFound`:** rejected by clarification.
- **Make `Destroy` non-idempotent (panic on double-destroy):**
  rejected — non-idiomatic Go; SDD-02's `Destroy` is idempotent
  and consumers will be habituated to that semantic.

---

## Decision 9 — 64 MiB load-side size cap + `ErrFileTooLarge`

**Decision:** `Load` pre-stats the file and rejects any path whose
`Size() > 64 * 1024 * 1024` with the new sentinel `ErrFileTooLarge`,
*before* any read or allocation.

**Rationale:**
- Spec Clarifications Q3: defends the production `Load` path
  directly rather than relying on the fuzz harness alone. Even with
  the parent-dir + file-mode checks in place, an attacker with
  write access to the vault file (e.g. a misconfigured backup tool
  rewriting `~/.hush/secrets.vault`) could otherwise force the
  server to read a multi-gigabyte file into memory at startup.
- 64 MiB is comfortably above the in-scope worst case (≈500
  secrets × multi-kilobyte values ≈ a few MiB plaintext, encrypted
  with constant 16-byte tag overhead). It also bounds the per-call
  memory footprint deterministically for the fuzz test.
- The check happens at `os.Stat` time (cost: one syscall), before
  any `os.Open` or `io.ReadAll`, so the resource cost of a refused
  file is minimal.

**Alternatives considered:**
- **No size cap (rely on the OS / Go's `io.ReadAll` defaults):**
  rejected by clarification.
- **Streaming AEAD (chunked decryption):** rejected — adds an
  entire parser surface for chunk framing, a new fuzz target, and
  buys nothing for the in-scope payload sizes. Out of scope per
  Spec "Out of Scope" (no streaming/page-by-page decryption).
- **A smaller cap (1 MiB, 16 MiB):** rejected — would refuse a
  legitimate large-payload vault; the 64 MiB ceiling matches the
  spec's clarification answer.

---

## Decision 10 — Save-side input validation (`ErrDuplicateName`, `ErrInvalidName`)

**Decision:** Before any encryption or filesystem touch, `Save`
runs a single pre-pass over its `[]Secret` input that:
1. Records each name in a `map[string]struct{}`; any name seen
   twice triggers `ErrDuplicateName` immediately.
2. For each entry, validates `Name` (non-empty, ≤256 bytes,
   printable ASCII `0x20`–`0x7E`, no control/NUL bytes) and
   `Description` (≤4096 bytes, no `0x00`–`0x1F`, no `0x7F`); any
   violation triggers `ErrInvalidName` immediately.
The pre-pass produces no working file and writes nothing to disk.

**Rationale:**
- Spec Clarifications Q2 + Q5: both classes of input bug must be
  caught before encryption, by typed sentinels distinct from any
  filesystem error, with no working file left behind.
- Validating *before* encryption avoids the failure mode where a
  caller fixes the input and retries, producing a sequence of
  partially-written vaults with bumped (key, nonce) pairs — the
  AEAD guarantee is fine, but the operator-visible churn is not.
- The constraints are tight enough to close log-injection and
  display-corruption hazards in every downstream renderer (CLI
  list, HTTP handler, structured-log line) without becoming a
  Unicode-policy maze.

**Alternatives considered:**
- **Allow Unicode in names/descriptions, with a normalisation
  pass:** rejected — adds a Unicode dependency surface (NFC
  normalisation), and the project's actual secret names are all
  ASCII identifiers (`ANTHROPIC_API_KEY` etc.).
- **Validate during JSON marshalling instead of in a pre-pass:**
  rejected — `encoding/json` does not surface field-level
  semantic errors; the resulting error would be opaque, hard to
  classify, and would only fire after the marshal had allocated
  the entire payload buffer.

---

## Decision 11 — `Names()` returns a defensive copy in stable load order

**Decision:** The store stores names in a `[]string` in the order
they appeared in the decrypted JSON array. `Names()` returns a copy
of that slice (via `append([]string(nil), names...)`) per call.

**Rationale:**
- Spec FR-025 + SC-012: stable order, no value material in the
  output.
- A defensive copy prevents an external `slices.Sort` from
  corrupting the canonical order observed by other consumers — at a
  per-call cost of one slice allocation, which is acceptable for
  the in-scope scale (≤500 secrets).

**Alternatives considered:**
- **Return the internal slice directly:** rejected — too easy for
  a caller to mutate.
- **Return a `[]string` of a `chan` or an iterator:** rejected —
  over-engineering for the scale.

---

## Decision 12 — Concurrency: `sync.RWMutex` over a single map

**Decision:** `memStore` holds one `sync.RWMutex`. `Get` and
`Names` take `RLock`; `Destroy` takes `Lock`. The internal
`map[string]*SecureBytes` is read-only after `Load` and is treated
as immutable (the store is constructed once, served until
`Destroy`); the mutex protects only the `destroyed` flag and the
`Destroy` traversal.

**Rationale:**
- Spec FR-026 + SC-010: many goroutines concurrent on `Get`,
  zero data races under `-race`. `RWMutex` allows arbitrary
  read concurrency on `Get` (the dominant workload — server's
  secret-fetch handler).
- The map is only written to during `Load`'s construction phase,
  before the store pointer is shared with any consumer. Once
  `Load` returns, the map is read-only — no mutex needed for the
  map itself, only for the `destroyed` flag.
- The destruction-then-`Get` race is documented in `spec.md`
  ("either outcome is safe; what the store must not do is return
  a partially-zeroed payload"). The `RWMutex` plus the SDD-02
  guarantee that `*SecureBytes.Use` holds its own internal mutex
  for the duration of the borrow combine to satisfy this: a `Get`
  that reads a not-yet-destroyed pointer and races against
  `Destroy()` will either complete its `Use(fn)` callback fully
  (last-retrieval-wins) or get `ErrDestroyed` from the inner
  container (which we map to a fresh-`SecureBytes`-construction
  failure — see Decision 13).

**Alternatives considered:**
- **`sync.Mutex` only:** rejected — needlessly serialises
  concurrent reads.
- **`atomic.Pointer[map[string]*SecureBytes]`:** rejected — adds
  copy-on-destroy complexity for no gain; the map is immutable
  after `Load` so a vanilla read does not need atomic ordering.
- **No locking, rely on the map's read-only-after-publish
  property:** rejected — the `destroyed` flag is mutable and
  observed concurrently; without the mutex, post-destroy `Get`
  would race the flag.

---

## Decision 13 — Mapping `securebytes.ErrDestroyed` from `Get`'s inner copy path

**Decision:** When `Get(name)` enters `Use(fn)` on the internal
container and `Use` returns `ErrDestroyed` (race with `Destroy`),
the `Get` call returns `ErrStoreDestroyed`. This unifies the user-
facing failure mode regardless of whether the store-level
`destroyed` flag was observed first or the per-container destroy
was observed first.

**Rationale:**
- Spec edge case "Concurrent retrievals during destruction": "the
  store either still serves the secret … or returns the named
  'destroyed' failure (destroy wins). Either outcome is safe."
- A single sentinel for the entire post-destroy failure mode
  matches the operator-facing classification model (Constitution
  V — loud, distinct, actionable failure).

**Alternatives considered:**
- **Return the inner `securebytes.ErrDestroyed` verbatim:**
  rejected — leaks an internal sub-package error to consumers,
  who would then have to compare against TWO errors for "store
  destroyed". The sub-package error is wrapped, not surfaced.

---

## Decision 14 — Fuzz target shape (`FuzzVaultDecode`)

**Decision:** `FuzzVaultDecode(f *testing.F)` seeds the corpus
with the byte stream produced by the round-trip test fixtures (a
known-good envelope produced by `Save`), plus a curated set of
truncated and bit-flipped variants. The fuzz function calls
`Load(ctx, path, key)` against a temp file populated with the
random byte stream and asserts:
1. No panic occurred (Go's testing framework already enforces
   this by design — included in the assertion table for
   completeness).
2. If `Load` returned an error, that error is one of the ten
   typed sentinels (or a wrapper around one — `errors.Is` against
   each).
3. Allocation during the call did not exceed 50 MiB
   (verified via `runtime.MemStats` deltas across the call).

**Rationale:**
- Constitution VIII names "vault file decode" as mandatory fuzz
  target #1; the SDD-03 chunk contract names this fuzz target
  and the 60-second / 50 MiB / typed-error gates explicitly.
- Seeding from real round-trip fixtures gives the fuzzer a
  productive starting point (the format is structured enough that
  pure-random bytes hit `ErrBadMagic` on essentially every
  iteration).
- The 50 MiB ceiling is well below the production 64 MiB cap
  (Decision 9) — the fuzz harness should never approach the
  production cap by construction.

**Alternatives considered:**
- **Pure-random corpus (no seeding):** rejected — wastes most
  of the fuzz budget hitting the magic check.
- **Run the fuzzer against `Save` instead:** rejected — `Save`'s
  inputs are typed (`[]Secret`) so there is no parser surface to
  fuzz. The parser surface, and therefore the panic risk, lives
  in `Load`.

---

## NEEDS CLARIFICATION audit

There are no `[NEEDS CLARIFICATION]` markers in `spec.md`. The
five clarifications surfaced by `/speckit-clarify` were resolved
in `spec.md`'s Clarifications block before this plan was drafted;
the resolution of each appears in the Decisions above:

| Clarification | Plan decision |
|---------------|---------------|
| Q1 — post-destroy `Get` semantics | Decision 8 (`ErrStoreDestroyed`) |
| Q2 — duplicate-name handling on `Save` | Decision 10 (`ErrDuplicateName` pre-pass) |
| Q3 — load-side size cap | Decision 9 (64 MiB + `ErrFileTooLarge`) |
| Q4 — best-effort tmp cleanup on error | Decision 5 (atomic-write strategy) |
| Q5 — name/description validation | Decision 10 (`ErrInvalidName` pre-pass) |

Phase 0 is complete.
