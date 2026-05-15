# Feature Specification: Vault File Format and In-Memory Store

**Feature Branch**: `003-vault-format`
**Created**: 2026-04-27
**Status**: Draft
**Input**: User description: "internal/vault: load and save the hush vault file (binary HUSH format, AES-256-GCM, atomic write with 0600 mode and 0700 parent), and serve secrets as SecureBytes-wrapped values via an in-memory Store"

## Clarifications

### Session 2026-04-27

- Q: How should `Store.Get` behave after `Store.Destroy()` has been called? → A: Add a new sentinel `ErrStoreDestroyed` as a distinct, programmatically-distinguishable failure mode (option A); FR-028 list grows to 7 entries and the chunk-contract exported-API list is amended at impl time.
- Q: How should `Save` handle a duplicate name in its input list? → A: Reject up-front with a new typed sentinel `ErrDuplicateName` before any encryption or write occurs (option A); FR-028 list grows by one; pre-pass de-duplication check runs before the AES-GCM seal step.
- Q: Should `Load` enforce a hard upper bound on vault file size before reading? → A: Yes — pre-stat the file and reject anything larger than 64 MiB with a new typed sentinel `ErrFileTooLarge` before any allocation or read (option A); FR-028 list grows by one; this defends the production Load path directly rather than relying on the fuzz harness alone.
- Q: Should `Save` clean up its own working file on a controlled mid-flight error (fsync failure, seal failure, post-tmp-create permission check failure)? → A: Yes — best-effort `os.Remove(<path>.tmp)` on every error path inside `Save` (option A); remove-error itself is logged but not surfaced; the SIGKILL case still leaves orphans for the upstream sweep.
- Q: Should the spec define minimal validation rules for the `name` and `description` fields of a secret entry? → A: Yes — enforce at the vault package boundary (option A): name is non-empty, ≤256 bytes, printable ASCII excluding control/NUL; description is ≤4096 bytes, no NUL/control; violations return a new typed sentinel `ErrInvalidName`; checked in `Save` alongside the duplicate-name pre-pass.

## Overview

The `internal/vault` package owns the on-disk vault file: a binary
HUSH-format envelope encrypted with AES-256-GCM, written atomically,
strictly file-mode-enforced, and decoded into an in-memory store that
hands out secrets wrapped in mlocked, redaction-protected containers
(the Layer 5 secure-memory primitive established by SDD-02).

The package is the single integration point between persistent secret
custody (the encrypted file on the trusted host) and live secret
delivery (the running server, the rotation CLI, the lifecycle
harness). It is consumed by the server's startup and SIGHUP reload
path (SDD-10), the server's secret-fetch handler (SDD-13), the
operator's rotation CLI (SDD-17), and the integration harness
(SDD-25). Its acceptance criterion is **AC-2 (vault round-trip;
SIGHUP reload remains SDD-10's half)**.

## User Scenarios & Testing *(mandatory)*

### User Story 1 — Persist a set of named secrets so they survive restart but are unreadable without the right key (Priority: P1)

The operator has a set of named, described secret values (an API
token, an OAuth refresh token, a deploy key) that must outlive the
process lifetime so the server can reload them after a crash, an
update, or a deliberate restart. The same set must be impossible to
read back without the correct vault encryption key — both because the
file is at rest on a host that may, under a worst-case incident, be
imaged or copied, and because the storage layer is the perimeter of
the encryption guarantee.

**Why this priority**: Without persistence, the server has no
secrets to broker. Without authenticated encryption, persistence is
worse than memory-only because the failure mode is silent: an
attacker who copied the file goes undetected. This story is the
precondition for every other vault-related capability.

**Independent Test**: A test pairs a freshly-derived encryption key
with a list of named secrets, saves them to a file, reopens the file
with the same key, and confirms every name, description, and value
round-trips exactly. The same test then attempts to reopen the file
with a different key and confirms the operation fails with a
distinct, named authentication failure — the secret values do not
appear in the failure output.

**Acceptance Scenarios**:

1. **Given** an empty list of secrets and a valid encryption key,
   **When** the list is saved and the file is reloaded with the same
   key,
   **Then** the in-memory store reports zero names and the file on
   disk has the project's standard envelope structure.
2. **Given** a list of one to many hundreds of named secrets and a
   valid encryption key,
   **When** the list is saved and the file is reloaded with the same
   key,
   **Then** the in-memory store reports exactly the saved names in
   stable order, every name and description round-trips byte-for-byte,
   and a borrow read of each secret's payload yields the original
   value byte-for-byte.
3. **Given** a saved vault and an encryption key that does not match
   the one used to save,
   **When** the file is reloaded,
   **Then** the operation returns the named "authentication failed"
   failure, no secret value is exposed in the failure output or in
   any captured log line, and the in-memory store is not constructed.

---

### User Story 2 — Serve secrets to internal consumers as redaction-protected, individually-owned containers (Priority: P1)

Once the vault has been loaded, multiple internal consumers (the
secret-fetch handler servicing HTTP requests, the rotation CLI
checking for a name collision, the lifecycle harness exercising a
round-trip) need to retrieve specific secrets by name. Each consumer
must receive a container it can independently destroy on its own
boundary (handler return, command exit, scenario cleanup) without
affecting any other consumer's view of the same secret. The store
must remain usable for further retrievals after any individual
consumer destroys its own copy.

**Why this priority**: Layer 5 (secure memory) is the project's
defence against accidental plaintext disclosure through logs,
formatting, JSON encoding, and forgotten heap allocations. A read
path that handed back a shared, mutable container would couple the
lifecycle of every consumer together — one consumer destroying its
copy would zero every other consumer's view. The store must
decouple them.

**Independent Test**: A test loads a vault holding a known sentinel
byte sequence, retrieves the secret twice in two independent flows,
destroys the first retrieved container, and confirms the second
container still yields the sentinel via a borrow read. A separate
concurrency test issues many simultaneous retrievals from many
goroutines and confirms each call returns a container holding the
correct payload, with no data race observable under race-detector
instrumentation.

**Acceptance Scenarios**:

1. **Given** a loaded vault with at least one named secret,
   **When** a consumer retrieves the secret by name,
   **Then** the call returns a fresh, independently-owned secure
   container whose borrow read yields the stored value byte-for-byte,
   and whose destruction has no observable effect on any subsequent
   retrieval of the same name.
2. **Given** a loaded vault and a name that is not in it,
   **When** a consumer retrieves by that name,
   **Then** the call returns the named "secret not found" failure
   without exposing the names of any other secrets in the store.
3. **Given** a loaded vault and many goroutines concurrently
   retrieving the same and different names,
   **When** the test runs under race-detector instrumentation,
   **Then** every retrieval succeeds with the correct payload and no
   data race is reported.
4. **Given** a loaded vault,
   **When** the consumer asks the store for the list of names it
   holds,
   **Then** the store reports exactly the saved names with no value
   material in the response.
5. **Given** a loaded vault,
   **When** the consumer destroys the entire store,
   **Then** every internally-held secure container is zeroed and any
   subsequent retrieval returns a distinct failure indicating the
   store is no longer usable.

---

### User Story 3 — Replace the vault on disk atomically so readers never see a torn file (Priority: P1)

The operator periodically rotates a secret (a new API token from the
upstream provider, a regenerated deploy key) by writing a new vault
that differs from the previous one in exactly one entry. While the
write is in flight, another process — most importantly the running
server — may attempt to reload the vault. The reload must observe
either the complete previous vault or the complete new vault — never
a partially-written, corrupted, or zero-length file.

**Why this priority**: AC-2 of the project's release gate names this
specifically: "atomic swap of the rotated value with no in-flight
request failures". A torn write would manifest in production as an
intermittent server-startup failure or a SIGHUP-reload failure that
is hard to reproduce. Atomicity at the filesystem level eliminates
the failure mode at the source.

**Independent Test**: A test saves a baseline vault, then runs a
second save in the same directory under deliberate filesystem
inspection — at every observable instant during the second save, the
target path either contains the complete original ciphertext or the
complete new ciphertext, and no intermediate temporary file at the
target path is ever observable to a parallel reader. After the second
save completes, the file's contents match the new vault and any
intermediate file used during the write is gone.

**Acceptance Scenarios**:

1. **Given** an existing vault file on disk,
   **When** a save replaces it with a new vault,
   **Then** at every instant during the save, a parallel open of the
   target path either yields the complete previous file or the
   complete new file, and never a zero-length, truncated, or
   syntactically-invalid file.
2. **Given** an existing vault file on disk,
   **When** a save fails partway through (for example, the disk
   refuses the write),
   **Then** the original file at the target path is unchanged, no
   committed-but-corrupt file replaces it, and any temporary working
   file is the only artefact left behind.
3. **Given** a vault has just been saved,
   **When** the file's mode is inspected,
   **Then** the mode is the project's standard secrets-file mode and
   not laxer.

---

### User Story 4 — Refuse to operate on a vault file with loose filesystem permissions (Priority: P1)

The vault file is the most security-sensitive artefact the host
produces. If the operator (or, more commonly, a misconfigured backup
tool, an errant `chmod`, or a careless install script) leaves the
file or its parent directory readable by other accounts on the host,
the entire encryption guarantee is undermined: the file is now
exposed at rest to any local actor. Both save and load must enforce
the project's standard-secret-file mode policy and refuse to operate
on a file or directory that is laxer.

**Why this priority**: The project's security model places the file
permission check inside the package that owns the file, not in a
review checklist. AC-8 of the release gate explicitly names "refuse
to start if any file in the secrets directory is more permissive
than the standard mode". This story is that enforcement, half of it
on the load path and half on the save path.

**Independent Test**: A test creates a vault file and parent
directory at deliberately-laxer-than-required modes, attempts to
load the file, and confirms the load returns the named
"permissions loose" failure without decrypting or constructing the
in-memory store. The same test then constructs a fresh save target
under a parent directory whose mode is laxer than required and
confirms the save returns the same named failure without writing
the new vault.

**Acceptance Scenarios**:

1. **Given** a syntactically-valid vault file whose mode is laxer
   than the project's standard secrets-file mode,
   **When** the file is loaded,
   **Then** the load returns the named "permissions loose" failure,
   no decryption is attempted, and no in-memory store is
   constructed.
2. **Given** a syntactically-valid vault file whose parent directory
   mode is laxer than the project's standard secrets-directory mode,
   **When** the file is loaded,
   **Then** the load returns the named "permissions loose" failure
   without attempting decryption.
3. **Given** a save target whose parent directory mode is laxer than
   the project's standard secrets-directory mode,
   **When** a save is attempted,
   **Then** the save returns the named "permissions loose" failure
   without writing the new vault.
4. **Given** a save that succeeds,
   **When** the produced file's mode is inspected,
   **Then** the file mode is the project's standard secrets-file
   mode regardless of whatever mode the parent directory's umask
   would otherwise have produced.

---

### User Story 5 — Distinguish failure modes precisely so operators see specific errors instead of a generic "vault failed" (Priority: P2)

When the load path rejects a file, the operator (or the launchd /
systemd unit, or the bootstrap runbook) needs to know specifically
which check failed: the file is not a vault file at all (wrong
magic), the vault is from a future or unsupported file-format
version, the file is truncated below one of the header boundaries,
the encryption key does not match, or the permissions are too lax.
Each of these has a different remediation, and a generic failure
forces the operator to guess.

**Why this priority**: Loud failure is a constitutional principle
(Principle V — "staleness is visible, failure is loud"). An
indistinguishable failure is, in operational practice, a silent
failure. Precise classification is also a release-gate property:
fuzz target #1 in the project's testing strategy explicitly requires
that every load-path error path produce a typed failure rather than
a panic or an opaque error string.

**Independent Test**: A test produces, for each named failure mode,
a minimal input that triggers that mode (a file with the wrong
identifying header bytes, a file with an unsupported version byte,
a file truncated at each header boundary, a file decrypted with the
wrong key, a file at laxer-than-required mode), and asserts that
each load attempt returns the failure named for that specific mode
— and that each named failure is programmatically distinguishable
from every other named failure in the package's surface.

**Acceptance Scenarios**:

1. **Given** a file whose first four bytes do not match the project's
   vault-file identifying bytes,
   **When** the file is loaded,
   **Then** the load returns the named "magic mismatch" failure.
2. **Given** a file whose identifying bytes match but whose version
   byte is not the version this package supports,
   **When** the file is loaded,
   **Then** the load returns the named "version mismatch" failure.
3. **Given** a file truncated below the size required to contain the
   identifying header, the version byte, the salt, the nonce, or
   the minimum authenticated-ciphertext size,
   **When** the file is loaded,
   **Then** the load returns the named "short header" failure.
4. **Given** a file whose header is well-formed but whose ciphertext
   has been tampered with or whose encryption key does not match,
   **When** the file is loaded,
   **Then** the load returns the named "authentication failed"
   failure, and the failure output contains no payload bytes and no
   stored secret name.

---

### Edge Cases

- **Empty vault (zero secrets)**: A vault with an empty list of
  secrets must save and load without special-casing — the round-trip
  yields a store reporting zero names and a borrow read of any name
  returns the "secret not found" failure.
- **Largest in-scope vault (hundreds of secrets)**: A vault with at
  least 500 named secrets must round-trip exactly, with stable name
  ordering and all values intact.
- **Empty secret value**: A secret whose value is a zero-length byte
  sequence must save and load — the borrow read of its payload
  yields a zero-length buffer.
- **Long secret value**: A secret whose value is a multi-kilobyte
  byte sequence (for example, a private-key bundle) must save and
  load with the value intact.
- **Duplicate names within a save call**: Two entries in the input
  list that share a name MUST cause `Save` to return the named
  "duplicate name" failure before any encryption is performed and
  before any file (including the working file) is written. The
  failure text MAY name the duplicated key; it MUST NOT include any
  secret value.
- **Get on the empty string**: Treated as any other unknown name —
  returns the "secret not found" failure.
- **Concurrent retrievals during destruction**: The behaviour of the
  store after destruction is "no further retrievals succeed". A
  caller racing destruction against retrieval may observe either
  outcome — the store either still serves the secret (last
  retrieval wins) or returns the named "destroyed" failure (destroy
  wins). Either outcome is safe; what the store must not do is
  return a partially-zeroed or otherwise corrupted payload.
- **Save's intermediate working file is left behind**: If the
  save process is killed (SIGKILL, host crash, OOM) between
  writing the working file and committing it, the working file
  remains on disk but the target path is unchanged. The next save,
  init, or cleanup pass clears the orphan; the package itself does
  not enumerate or remove prior orphans. For a controlled mid-flight
  error (fsync failure, seal failure, post-tmp-create permission
  check failure), the package DOES attempt a best-effort removal of
  its own working file — see FR-013.
- **Symlink at the target path**: Out of scope for this package —
  the parent layers (init, secret-CLI) place the vault file at the
  documented path; this package treats the target path as a regular
  file and does not attempt to resolve symlinks before mode-checks.
- **Sentinel-leak on authentication failure**: A vault saved with a
  sentinel byte sequence as one of its secret values, then loaded
  with the wrong encryption key, must produce a failure whose
  rendered text contains zero occurrences of the sentinel and whose
  captured log output contains zero occurrences of the sentinel.
- **Get is called after the store has been destroyed**: Returns
  the named "store destroyed" failure (distinct from "secret not
  found"); no payload is materialised.

## Requirements *(mandatory)*

### Functional Requirements

**On-disk envelope**

- **FR-001**: The vault file MUST begin with a fixed 4-byte
  identifying header — the bytes `0x48 0x55 0x53 0x48` (the ASCII
  sequence `"HUSH"`). Any file whose first 4 bytes differ MUST be
  rejected on load with the named "magic mismatch" failure.
- **FR-002**: The 4-byte identifying header MUST be immediately
  followed by a single version byte. The version byte for the
  v0.1.0 file format MUST be `0x01`. Any file whose version byte
  differs MUST be rejected on load with the named "version mismatch"
  failure.
- **FR-003**: The version byte MUST be immediately followed by a
  16-byte field reserved for the key-derivation salt used by the
  upstream layer that produces the encryption key. The salt MUST be
  filled by the save path with bytes obtained from a
  cryptographically-secure random source; on load, the field MUST
  be carried back to the upstream layer (re-derivation is not the
  responsibility of this package).
- **FR-004**: The salt field MUST be immediately followed by a
  12-byte field holding the authenticated-encryption nonce. The
  nonce MUST be filled by the save path with bytes obtained from a
  cryptographically-secure random source, freshly drawn for every
  save call.
- **FR-005**: The nonce field MUST be immediately followed by the
  authenticated ciphertext (ciphertext-with-authentication-tag) of
  the plaintext payload. The authenticated-encryption algorithm
  used to produce and verify this field MUST be AES-256-GCM.
- **FR-006**: The on-disk file MUST contain no fields beyond those
  enumerated in FR-001 through FR-005 — no padding, no separator
  bytes, no trailing length field, no signature appended after the
  ciphertext. The file's exact length is `4 + 1 + 16 + 12 +
  len(ciphertext-with-tag)`.

**Plaintext payload**

- **FR-007**: The plaintext payload MUST be the encoding of an
  ordered list of zero or more secret entries.
- **FR-008**: Each secret entry MUST carry exactly three fields: a
  human-readable name (the identifier callers use to retrieve it),
  a human-readable description (operator-facing context), and a
  binary value (the secret material). The name MUST be non-empty,
  at most 256 bytes long, and consist only of printable ASCII
  characters (byte values `0x20`–`0x7E` inclusive); control
  characters and NUL bytes are forbidden. The description MUST be
  at most 4096 bytes long and MUST NOT contain NUL bytes or other
  ASCII control characters (byte values `0x00`–`0x1F` and `0x7F`).
  These rules close a log-injection / display-corruption class for
  every downstream renderer (CLI listings, HTTP handlers,
  structured-log lines).
- **FR-009**: At no point during save or load MUST a secret value
  be materialised as a string-typed value. The decode path MUST
  place each secret value directly into a secure container (the
  Layer 5 primitive defined in SDD-02) without an intermediate
  string allocation. This is a security invariant of the package,
  not just an implementation preference: a string-typed value
  cannot be reliably zeroed and would defeat the whole Layer 5
  protection.
- **FR-010**: The plaintext payload MUST be small enough to
  comfortably hold at least 500 secrets, each with a value up to
  several kilobytes long, without architectural change to the
  package.

**Save path**

- **FR-011**: The save path MUST accept a target file path, an
  encryption key carried in a secure container (Layer 5), and an
  ordered list of secret entries.
- **FR-012**: The save path MUST commit the new file atomically:
  at every instant during the call, a parallel reader of the
  target path MUST observe either the complete previous file (if
  any) or the complete new file, and never an intermediate state.
- **FR-013**: If the save path fails partway through (any
  filesystem-level failure during the write or commit), the file
  at the target path MUST be unchanged from its pre-call state.
  No partial or zero-length file MUST replace it. Additionally,
  the save path MUST attempt a best-effort removal of any working
  file it created (typically `<path>.tmp` in the target directory)
  before returning the error. A failure of this best-effort
  removal MUST NOT be surfaced to the caller and MUST NOT mask the
  original error; it MAY be logged. The SIGKILL case (process
  death between working-file creation and rename) is unchanged: the
  working file remains for the upstream cleanup sweep.
- **FR-014**: After a successful save, the file at the target path
  MUST have mode `0600` (owner read+write only). The mode MUST be
  set by the save path itself rather than left to the umask.
- **FR-015**: Before writing, the save path MUST verify that the
  parent directory of the target path has mode `0700` (owner
  read+write+execute only). A laxer parent mode MUST cause the
  save to return the named "permissions loose" failure without
  writing.
- **FR-016**: The salt and nonce written into the on-disk header
  MUST come from a cryptographically-secure random source on every
  save call. They MUST NOT be reused across save calls and MUST
  NOT be derived from the system clock, the process ID, or any
  other non-cryptographic source.

**Load path**

- **FR-017**: The load path MUST accept a target file path and an
  encryption key carried in a secure container (Layer 5), and on
  success MUST produce an in-memory store from which secrets can
  be retrieved by name.
- **FR-018**: Before opening the file's contents, the load path
  MUST stat the target path and refuse to proceed if its mode is
  laxer than `0600`. The failure MUST be the named "permissions
  loose" failure.
- **FR-019**: The load path MUST also stat the parent directory and
  refuse to proceed if its mode is laxer than `0700`. The failure
  MUST be the same named "permissions loose" failure.
- **FR-019a**: As part of the same stat call, the load path MUST
  reject any file whose size exceeds 64 MiB (67,108,864 bytes)
  with the named "file too large" failure, before any read or
  allocation of the file's contents. The cap is chosen to comfortably
  exceed the in-scope worst case (≈500 secrets × multi-kilobyte
  values) while bounding the per-call memory footprint of an
  attacker-controlled file at the vault path.
- **FR-020**: The load path MUST validate the on-disk envelope in
  order: identifying header → version byte → minimum length to
  contain salt + nonce + minimum authenticated ciphertext. A
  failure at any of these boundaries MUST produce the corresponding
  named failure ("magic mismatch", "version mismatch", or
  "short header") and MUST NOT attempt decryption.
- **FR-021**: On a successful header validation, the load path MUST
  attempt authenticated decryption with the supplied encryption
  key. A failure to verify the authentication tag (whether caused
  by a wrong key, by tampering with the ciphertext, or by truncation
  of the ciphertext below the minimum authenticated size) MUST
  produce the named "authentication failed" failure.
- **FR-022**: The load path MUST decode the plaintext payload into
  the in-memory store such that every secret value is, from the
  moment it is decrypted, held only in a Layer 5 secure container.
  No code path on the load side MUST hand a plaintext secret value
  to a string-typed allocation.

**In-memory store**

- **FR-023**: The store MUST expose a retrieve-by-name operation.
  A successful retrieval MUST return a fresh, independently-owned
  Layer 5 secure container holding the requested secret's value;
  destruction of the returned container MUST NOT affect any other
  consumer's view of the same secret, nor any subsequent retrieval
  of the same name.
- **FR-024**: A retrieve-by-name operation against a name not in
  the store MUST return the named "secret not found" failure
  without exposing the names of any other secrets.
- **FR-025**: The store MUST expose a names operation that returns
  the list of secret names it holds, in the same stable order they
  appeared in the file. The names operation MUST NOT return any
  value material.
- **FR-026**: The store MUST support concurrent retrieve-by-name
  operations from many callers simultaneously without data races
  observable under race-detector instrumentation.
- **FR-027**: The store MUST expose an explicit destroy operation
  that zeroes every internally-held secure container. After
  destruction, retrieve-by-name operations MUST return the named
  "store destroyed" failure (programmatically distinguishable from
  every other named failure on the package's surface); the
  destruction MUST be idempotent.

**Failure classification**

- **FR-028**: The package MUST expose at minimum the following
  programmatically-distinguishable failure modes, each named so a
  caller can detect that specific mode without parsing free-form
  error text:
  - magic mismatch (file does not start with the project's
    identifying header)
  - version mismatch (header is correct but the version byte is
    not supported by this package)
  - short header (file is shorter than the minimum bytes required
    to contain the header, salt, nonce, or minimum authenticated
    ciphertext)
  - authentication failed (header validates but authenticated
    decryption fails — wrong key, tampering, or ciphertext
    truncation)
  - permissions loose (the file mode or its parent directory mode
    is laxer than this package permits)
  - secret not found (a retrieve-by-name targets a name that is
    not in the loaded store)
  - store destroyed (a retrieve-by-name targets a store on which
    the destroy operation has already been called)
  - duplicate name (a save call's input list contains two or more
    entries that share a name)
  - file too large (the file at the load target path exceeds the
    package's per-call size cap of 64 MiB)
  - invalid name (a save call's input list contains an entry whose
    name or description violates the FR-008 constraints)
- **FR-029**: Every failure path on the load and retrieve surface
  MUST produce one of these named failures (or a wrapped
  underlying I/O failure with one of these as its top-level
  classification). A panic on any reachable input MUST NOT occur.
- **FR-030**: No failure-mode rendered text and no log line emitted
  by the package MUST contain any byte of any secret value or any
  derivation of any secret value (length-only summary, prefix,
  suffix, or hash). Failure text MAY name the failure mode, the
  file path, and the secret name; it MUST NOT include the secret
  value.

**Redaction discipline**

- **FR-031**: Every secret value in motion through this package —
  during decode, during in-memory residence in the store, during
  retrieval — MUST sit inside a Layer 5 secure container, so that
  the project's standard logging, formatting, and JSON-encoding
  paths render it as the literal string `[redacted]` per the
  invariants established in SDD-02.
- **FR-032**: The store and the entries it holds MUST never expose
  the raw byte sequence of a secret through any standard rendering
  path. The only path to a secret's payload is through the borrow
  read of the secure container returned by the retrieve-by-name
  operation.

**Input validation**

- **FR-033**: Before performing any encryption or filesystem write
  (including the working file used for the atomic-rename commit),
  the save path MUST scan its input list for duplicate names. If
  any name appears more than once, the save MUST return the named
  "duplicate name" failure and MUST NOT touch the filesystem. The
  failure text MAY identify the duplicated name; it MUST NOT
  include any secret value.
- **FR-034**: As part of the same pre-write validation pass, the
  save path MUST verify that every entry's name and description
  satisfy the constraints in FR-008 (name non-empty, ≤256 bytes,
  printable ASCII excluding control/NUL; description ≤4096 bytes,
  no NUL/control). Any violation MUST cause the save to return the
  named "invalid name" failure and MUST NOT touch the filesystem.
  The failure text MAY identify the offending field and entry
  position; it MUST NOT include any secret value.

### Key Entities

- **Vault file** — A single file at a documented path on the
  trusted host. Has these observable properties: a fixed
  identifying header (`HUSH`), a fixed version byte (`0x01`), a
  16-byte salt, a 12-byte authenticated-encryption nonce, and an
  authenticated ciphertext of the plaintext payload. Has these
  observable lifecycle states: absent, committed (mode `0600`,
  parent `0700`), and in-flight (the working file used during a
  save).
- **Secret entry** — One named, described, value-bearing record in
  the plaintext payload. Has these fields: name (identifier),
  description (operator-facing context), value (binary secret
  material). The name and description are operator-readable; the
  value lives only in a Layer 5 secure container from the moment it
  is decrypted on load to the moment it is destroyed on store
  shutdown.
- **In-memory store** — The result of a successful load. Has these
  observable operations: retrieve-by-name (returns a fresh,
  independently-owned secure container), names (returns the list of
  held names without value material), destroy (zeroes every
  internally-held secure container, idempotent). Has these
  observable lifecycle states: live (retrievals succeed), destroyed
  (retrievals return the named "store destroyed" failure).
- **Failure mode** — The package's classified, named outcome of any
  load or retrieve attempt that does not produce a usable result.
  Each named failure mode is programmatically distinguishable from
  every other and carries no payload material in its rendered text.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001 (Round-trip exactness)**: For every input list of named
  secrets in the size range of zero to at least five hundred
  entries, a save followed by a load with the same encryption key
  yields a store whose names appear in the original order, whose
  descriptions round-trip byte-for-byte, and whose values
  round-trip byte-for-byte under a borrow read.
- **SC-002 (Wrong-key rejection)**: A load attempt against a
  syntactically-valid vault using an encryption key different from
  the one that produced the file returns the named "authentication
  failed" failure and does not produce a store. The failure's
  rendered text and any log line emitted during the attempt
  contain zero bytes of any secret value.
- **SC-003 (Atomic save observable)**: At every instant during a
  save call that replaces an existing vault, a parallel reader of
  the target path reads either the complete previous file or the
  complete new file. No reader ever observes a zero-length, a
  truncated, or a syntactically-invalid file at the target path.
- **SC-004 (File mode enforced on save)**: Every file produced by a
  successful save call has mode `0600` regardless of the umask
  inherited by the calling process.
- **SC-005 (Parent mode enforced on save)**: A save targeting a
  directory whose mode is laxer than `0700` returns the named
  "permissions loose" failure and the target file is not written.
- **SC-006 (File mode enforced on load)**: A load against a file
  whose mode is laxer than `0600` returns the named "permissions
  loose" failure and no decryption is attempted.
- **SC-007 (Parent mode enforced on load)**: A load against a file
  whose parent directory mode is laxer than `0700` returns the
  named "permissions loose" failure and no decryption is attempted.
- **SC-008 (Header truncation classified)**: For a file truncated
  before the end of the identifying header, before the end of the
  salt, or before the end of the nonce, the load returns the
  named "short header" failure. For a file truncated within the
  minimum authenticated ciphertext, the load returns either the
  named "short header" failure (if below the AEAD minimum) or the
  named "authentication failed" failure (if at or above it).
- **SC-009 (Magic and version classified)**: A file whose first
  four bytes do not match the project's identifying bytes returns
  the named "magic mismatch" failure. A file whose version byte
  differs from the version this package supports returns the named
  "version mismatch" failure.
- **SC-010 (Concurrent retrieve is race-clean)**: Many goroutines
  performing retrieve-by-name against the same loaded store run
  to completion under race-detector instrumentation with zero
  reported data races.
- **SC-011 (Independently-owned containers)**: Destroying the
  container returned by one retrieve-by-name call has no
  observable effect on a container returned by a different
  retrieve-by-name call against the same name; subsequent
  retrievals continue to yield the correct value.
- **SC-012 (Names without values)**: The names operation returns
  the saved name list with no value bytes and no description bytes
  in its output.
- **SC-013 (Sentinel never leaks)**: A vault saved with a known
  sentinel byte sequence as one of its secret values, then loaded
  with the wrong encryption key, produces a failure whose rendered
  text and whose captured log output contain zero occurrences of
  the sentinel.
- **SC-014 (Fuzz-clean load path)**: A fuzz harness driving random
  byte sequences into the load path runs continuously for at least
  sixty seconds without producing a panic, without exceeding a
  bounded per-call memory ceiling, and with every reachable
  failure outcome classified into one of the named failure modes.

## Assumptions

- **Layer 5 primitive is available**: The package depends on the
  secure-container primitive established in SDD-02. Its memory
  protection, lifecycle protection, and render protection are
  preconditions, not duplicated here.
- **Encryption key is supplied by the caller**: This package does
  not derive the encryption key from a passphrase. The caller (the
  init flow on save, the server-startup or SIGHUP-reload flow on
  load) is responsible for producing the key — typically by
  applying the project's standard key-derivation function to a
  passphrase plus the salt carried in the file's header. The salt
  field is stored on this package's behalf for that upstream
  purpose; this package does not interpret it.
- **Atomic-rename semantics are available on the target
  filesystem**: The save path's atomicity guarantee relies on the
  target filesystem providing same-directory atomic rename
  semantics. The project's supported platforms (macOS HFS+ / APFS,
  Linux ext4 / XFS / btrfs / tmpfs) provide this; exotic
  filesystems are out of scope.
- **Single-writer discipline at the target path**: Two concurrent
  save calls against the same target path are an operator error
  and are not relied upon by callers. The PID-file / flock
  contract that prevents this is an upstream concern (the secret
  CLI / server lifecycle), not the responsibility of this
  package.
- **SIGHUP reload is out of scope here**: The hot-reload path
  (server detects SIGHUP, reloads the vault into a new in-memory
  store, atomically swaps live consumers over to the new store,
  destroys the old store) is the responsibility of SDD-10. This
  package provides the load primitive that SIGHUP reload is built
  on; it does not implement the swap itself.
- **Internal-only consumption**: The package is consumed only by
  other packages inside the project's `internal/` tree (server,
  secret CLI, lifecycle harness). It is not part of any external
  API contract.
- **Supported platforms**: macOS and Linux. Windows is out of
  scope for v0.1.0.

## Out of Scope

- Hot-reload of an already-loaded vault into a running server
  (SIGHUP swap, atomic-pointer publication, in-flight-request
  drain) — owned by SDD-10.
- Backup, replication, or off-host copying of the vault file —
  the file is explicitly ephemeral by Constitution Principle XI.
- Forward-compatible support for vault file format versions other
  than `0x01`. A future version increment is a separate release
  with its own migration story; this package supports exactly the
  current version.
- Encryption-key derivation from a passphrase (the Argon2id KDF
  step) — owned by SDD-01 (key derivation) and the init / startup
  flow that calls it.
- Detection or cleanup of orphaned working files left behind by a
  killed save — out of scope here; addressed by the rotation CLI's
  startup sweep.
- Symlink resolution or anti-symlink defences at the target path
  — the target path is treated as a regular file managed by the
  project's own init flow.
- Secret comparison primitives, secret diffing, or partial-update
  semantics. A save replaces the entire vault.
- Streaming or page-by-page decryption. The whole payload is
  loaded into memory at once; for the size range in scope (≤500
  secrets, multi-kilobyte values) this is comfortable.
- Multi-writer or cross-process locking of the vault file. The
  project's PID-file / flock discipline is owned upstream.

## Dependencies

- **SDD-02 (`internal/vault/securebytes`)** — the Layer 5
  secure-container primitive that holds every secret value handed
  out by this package. This package is blocked on SDD-02 being
  complete (it is — see `docs/AC-MATRIX.md` AC-7).
- **SDD-01 (`internal/keys`)** — supplies the encryption key (the
  vault key, derivation path `m/44'/7743'/1'`) that callers pass
  in to save and load. This package is blocked on SDD-01 being
  complete (it is — see `docs/AC-MATRIX.md` AC-7).
- **Constitutional principle III (Defence in depth — Encryption
  at Rest, Layer 5 secure memory)** — defines the security
  invariants this package must enforce.
- **Constitutional principle VIII (Testing Discipline)** — names
  this package as one of the four security-critical packages that
  must reach 100% coverage, and names "vault file decode" as
  mandatory fuzz target #1.
- **Constitutional principle X (Observability & Redaction)** —
  mandates the type-driven `[redacted]` rendering this package
  inherits from SDD-02 and the no-secret-in-error rule this
  package enforces on its own surface.
- **Constitutional principle XI (Native-First, Minimal
  Dependencies, Ephemeral Vault)** — bans new cryptographic
  dependencies; explicitly allows the AES-256-GCM and randomness
  primitives already in the project's allowed surface.
- **`docs/SPEC.md` FR-2, FR-10, FR-15** — the spec entries for
  the encrypted vault file, atomic writes plus reload, and the
  startup file-permissions check.
- **`docs/AC-MATRIX.md` AC-2** — the release-gate row this
  package's load and save tests directly contribute to (the
  SIGHUP-reload half of AC-2 remains owned by SDD-10).
- **Downstream packages blocked on this one**: SDD-10 (server
  startup + SIGHUP reload), SDD-13 (server secret-fetch handler),
  SDD-17 (`hush secret` rotation CLI), SDD-25 (lifecycle
  integration harness).
