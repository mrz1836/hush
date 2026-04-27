# Feature Specification: Hush Key Hierarchy Derivation

**Feature Branch**: `001-keys-derivation`
**Created**: 2026-04-27
**Status**: Draft
**Input**: User description: "internal/keys: derive the hush key hierarchy (JWT signing, vault encryption, audit signing, per-machine client keypair) deterministically from a passphrase + salt using Argon2id + BIP32"

## Overview

The hush product holds operator secrets in an encrypted vault and brokers
short-lived, human-approved access to them over Tailscale. Every cryptographic
operation in the system — vault encryption, JWT session signing, audit-log
signing, per-machine client request signing, and end-to-end ECIES transport —
depends on a key. Persisting any of those keys as a file on disk would
re-create the very dotfile-secret attack surface hush exists to eliminate.

This feature defines the **key-derivation surface** that every other hush
component builds on. It deterministically derives the entire hush key
hierarchy (a master seed plus four sub-keys: JWT-signing, vault-encryption,
audit-signing, per-machine client keypair) from two operator-supplied inputs
— a passphrase and a salt — using Argon2id as the master key-derivation
function and BIP32 hierarchical derivation for the sub-keys. The result is a
runtime-only key hierarchy: zero key files exist on disk, but the same
passphrase + salt always produce the same keys, which is what makes the
derivation reproducible across operator machines and across vault reloads.

The surface is consumed by the vault-encryption component (which needs the
vault encryption key), the JWT issuer (signing key), the audit-log writer
(audit signing key), the request-signing transport layer (per-machine
client keypair, indirectly), and the `hush init` bootstrap command. It is
the foundation of the seven-layer crypto stack documented in the project's
constitution; correctness here is load-bearing for every later acceptance
criterion that depends on a hush key.

## User Scenarios & Testing *(mandatory)*

### User Story 1 — Bootstrap: derive a fresh key hierarchy (Priority: P1)

As the hush operator, when I configure a fresh trusted host with a passphrase
and a freshly generated salt, the system MUST derive the full key hierarchy
deterministically so that subsequent vault encryption, JWT issuance, audit
signing, and client request signing all work without any key file existing
on disk.

**Why this priority**: Without this, no other hush feature works. Every
downstream component (vault, server, supervisor, audit, transport) depends on
having a master seed and the four documented sub-keys derivable from the
operator's inputs.

**Independent Test**: Provide a known-good passphrase (≥ 12 bytes) and a
16-byte salt; assert that the derivation returns a non-empty master seed
of the documented length and that each of the four sub-key derivations
succeeds. The hierarchy can be exercised entirely in-process — no network,
no disk persistence — making this story independently verifiable as a unit
test, which is what AC-7 requires.

**Acceptance Scenarios**:

1. **Given** a passphrase of ≥ 12 bytes and a 16-byte salt, **When** the
   master seed is derived, **Then** a 64-byte seed is returned with no error
   and no secret material is logged.
2. **Given** the same passphrase and salt are presented twice, **When** the
   master seed is derived each time, **Then** the two seeds are byte-for-byte
   identical (determinism).
3. **Given** a derived master seed, **When** the JWT signing sub-key is
   derived at the dedicated path, **Then** a usable secp256k1 signing key is
   returned.
4. **Given** a derived master seed, **When** the vault encryption sub-key is
   derived at the dedicated path, **Then** a 32-byte symmetric key suitable
   for AES-256 is returned.
5. **Given** a derived master seed, **When** the audit-signing sub-key is
   derived at the dedicated path, **Then** a usable secp256k1 signing key
   distinct from the JWT signing key is returned.

---

### User Story 2 — Per-machine client keys: machine-index isolation (Priority: P1)

As the hush operator, when I provision multiple agent machines from the same
passphrase + salt, each machine MUST receive a distinct client keypair
indexed by its assigned machine number, so that one compromised machine's
client key cannot impersonate another machine.

**Why this priority**: AC-6 in the project SPEC requires per-machine client
keys: distinct keys per machine index, and a different passphrase yields a
different key for the same index. The whole client-request signing layer is
built on this isolation property, and AC-7 (the primary criterion for this
chunk) depends on per-machine client keys to bind requests to their origin.

**Independent Test**: Derive client keys for machine indexes 0, 1, and 2
from the same master seed; assert all three keypairs are distinct. Then
re-derive index 0 from a master seed produced by a different passphrase;
assert the resulting key differs from the first index-0 key. No external
systems are involved.

**Acceptance Scenarios**:

1. **Given** a master seed and two distinct machine indexes, **When** client
   keys are derived for each index, **Then** the two private keys (and their
   public keys) are different.
2. **Given** the same machine index but two different passphrases (each
   producing a different master seed), **When** client keys are derived,
   **Then** the resulting keys are different.
3. **Given** the same passphrase, salt, and machine index, **When** the
   client key is derived twice, **Then** the two derivations produce
   identical keys (determinism preserved across machine-index dimension).

---

### User Story 3 — Input validation: hard-fail on weak or malformed inputs (Priority: P1)

As a security-conscious operator, when I (or a misbehaving caller) supply a
passphrase that is too short or a salt of the wrong length, the derivation
MUST refuse with a distinct, named error before performing any expensive
work, so that the system never produces a weak key and the caller can
distinguish the failure mode programmatically.

**Why this priority**: A weak or malformed input that is silently accepted
would reduce the entire hush threat model to the strength of whatever the
operator typed. Two specific failure modes — short passphrase, wrong-length
salt — are non-negotiable per the project constitution and the Security
Requirements table. Both must surface as named errors so callers (and
tests) can assert them.

**Independent Test**: Call the master-seed derivation with (a) a passphrase
of 11 bytes, (b) a salt of 8 bytes, (c) a salt of 24 bytes, and (d) a salt
of 0 bytes. Assert each call returns the expected named error and that the
key-derivation function (Argon2id) was not invoked (i.e. the rejection is
fast — does not perform the multi-second KDF).

**Acceptance Scenarios**:

1. **Given** a passphrase of fewer than 12 bytes, **When** master-seed
   derivation is invoked, **Then** a named "passphrase too short" error is
   returned without invoking the underlying key-derivation function.
2. **Given** a salt whose length is anything other than exactly 16 bytes
   (including 0, 8, 15, 17, 24, 32), **When** master-seed derivation is
   invoked, **Then** a named "salt missing or wrong length" error is
   returned without invoking the underlying key-derivation function.
3. **Given** a passphrase of exactly 12 bytes and a salt of exactly 16
   bytes, **When** master-seed derivation is invoked, **Then** the
   derivation succeeds (boundary case — 12 and 16 are inclusive lower /
   exact bounds).

---

### User Story 4 — Public-key fingerprint for client registration UX (Priority: P2)

As the hush operator registering a new agent machine in the server's
allowed-clients configuration, I need a short, stable, human-readable
identifier for a derived client public key so I can copy it into the config
file and visually confirm the same machine later.

**Why this priority**: This is a UX affordance for the `hush init --client`
flow, not a security boundary on its own. It is required by the chunk
contract for client-key registration ergonomics but does not block any
other behaviour. P2 because the hierarchy works without it, but the
registration flow needs it.

**Independent Test**: Compute the fingerprint twice for the same public key;
assert the two strings are identical, exactly 16 hexadecimal characters
long. Compute the fingerprint for two different public keys; assert the
two strings differ. No persistence or network involvement.

**Acceptance Scenarios**:

1. **Given** a derived public key, **When** the fingerprint helper is
   invoked, **Then** it returns a 16-character lowercase hexadecimal string.
2. **Given** the same public key presented twice, **When** the fingerprint
   helper is invoked each time, **Then** the two outputs are identical.
3. **Given** two distinct public keys, **When** the fingerprint helper is
   invoked on each, **Then** the two outputs differ.

---

### Edge Cases

- **Empty passphrase**: rejected as "passphrase too short" (0 < 12).
- **Empty salt**: rejected as "salt missing or wrong length" (0 ≠ 16).
- **Maximum machine index**: any 32-bit unsigned machine index value MUST
  produce a valid client keypair; the derivation MUST NOT silently truncate
  or wrap.
- **Boundary passphrase length (exactly 12 bytes)**: MUST be accepted.
- **Boundary salt length (exactly 16 bytes)**: MUST be accepted.
- **Determinism under concurrent calls**: parallel derivations from the
  same inputs MUST return identical outputs; no shared mutable state may
  cause divergence.
- **No partial success**: if any input validation fails, no derived material
  is returned to the caller — the error path leaks nothing about the
  passphrase or seed.
- **Cross-process determinism**: the same passphrase + salt on a different
  machine, on a different OS, on a different CPU architecture MUST produce
  the same master seed and the same sub-keys. The derivation is
  CPU-architecture-independent.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The system MUST derive a 64-byte master seed from a
  passphrase and a salt using Argon2id with the parameters
  `time = 4`, `memory = 256 × 1024 KiB` (256 MB), `threads = 4`,
  `keyLen = 64`. These parameters are non-negotiable and are mirrored in
  the project Security Requirements table; they MUST NOT be made
  configurable in this feature.
- **FR-002**: The system MUST be deterministic: the same passphrase + salt
  combination MUST produce the same master seed and the same sub-keys
  on every invocation, on every machine, on every OS, and on every CPU
  architecture supported by the project.
- **FR-003**: The system MUST reject any passphrase shorter than 12 bytes
  with a distinct, named error, before performing the key-derivation
  function.
- **FR-004**: The system MUST reject any salt whose length is not exactly
  16 bytes with a distinct, named error, before performing the
  key-derivation function.
- **FR-005**: The system MUST derive a JWT-signing key from the master
  seed at the hierarchical derivation path `m / 44' / 7743' / 0'`. The
  resulting key MUST be a secp256k1 private key suitable for ES256K
  signing.
- **FR-006**: The system MUST derive a vault-encryption key from the
  master seed at the hierarchical derivation path
  `m / 44' / 7743' / 1'`. The resulting key MUST be 32 bytes long,
  suitable as an AES-256 key.
- **FR-007**: The system MUST derive an audit-log-signing key from the
  master seed at the hierarchical derivation path
  `m / 44' / 7743' / 2'`. The resulting key MUST be a secp256k1 private
  key, distinct from the JWT-signing key, suitable for ECDSA signing of
  the hash-chained audit log.
- **FR-008**: The system MUST derive a per-machine client keypair from
  the master seed at the hierarchical derivation path
  `m / 44' / 7743' / 3' / {machine_index}`, where `{machine_index}` is
  an unsigned 32-bit integer supplied by the caller. Distinct machine
  indexes MUST yield distinct keypairs from the same master seed.
- **FR-009**: The system MUST expose a public-key fingerprint helper
  that, given a derived public key, returns a 16-character lowercase
  hexadecimal string. The fingerprint MUST be stable (same public key →
  same fingerprint) across processes, machines, and time. Different
  public keys MUST produce different fingerprints with overwhelming
  probability.
- **FR-010**: The system MUST NOT persist any derived material — master
  seed, sub-keys, public keys, or fingerprints — to disk, to environment
  variables, or to any external storage. All derived material lives in
  process memory only and is the caller's responsibility to handle.
- **FR-011**: The system MUST NOT log the passphrase, salt, master seed,
  or any derived key material in any form, at any log level, in any
  branch (success or error). Error messages MUST identify the failure
  mode without echoing input bytes.
- **FR-012**: Input validation (passphrase length, salt length) MUST run
  before the key-derivation function is invoked. A rejected call MUST
  return promptly (no multi-second delay caused by running Argon2id on
  invalid input).

### Key Entities

- **Passphrase**: operator-supplied byte sequence, length ≥ 12 bytes. The
  high-entropy input from which all hush keys are derived. Treated as
  secret material.
- **Salt**: 16-byte sequence stored with the vault file (per the project
  vault format). Combined with the passphrase to produce the master seed.
  Not secret on its own, but required for derivation to succeed.
- **Master seed**: 64-byte byte sequence produced by Argon2id from the
  passphrase + salt. The root of the hierarchical key tree. Treated as
  secret material; never persisted.
- **JWT signing key**: secp256k1 private key derived from the master
  seed at `m/44'/7743'/0'`. Used by the JWT issuer to sign session
  tokens with ES256K. Treated as secret material.
- **Vault encryption key**: 32-byte symmetric key derived from the
  master seed at `m/44'/7743'/1'`. Used by the vault component to
  encrypt and decrypt the vault payload with AES-256-GCM. Treated as
  secret material.
- **Audit signing key**: secp256k1 private key derived from the master
  seed at `m/44'/7743'/2'`. Used by the audit-log writer to ECDSA-sign
  hash-chained audit records. Treated as secret material; distinct from
  the JWT signing key.
- **Per-machine client keypair**: secp256k1 keypair derived from the
  master seed at `m/44'/7743'/3'/{machine_index}`. Used by an agent
  machine to sign requests to the vault server. The private half is
  treated as secret material; the public half is shared with the server
  in the registered-clients configuration.
- **Public-key fingerprint**: 16-character lowercase hexadecimal string
  derived from a public key. A non-secret, human-readable identifier
  used in operator-facing UX such as the `hush init --client`
  registration flow.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: 100% of unit tests over the key-derivation surface pass
  with the project race detector enabled. (This is the coverage bar
  required by the project constitution for security-critical packages.)
- **SC-002**: A 60-second fuzz run against the master-seed derivation
  produces zero panics, zero crashes, and zero divergent re-derivations
  for the same input pair (i.e. the deterministic property holds under
  random valid inputs).
- **SC-003**: The same passphrase + salt produces byte-for-byte identical
  master seeds and sub-keys when re-derived in a different process,
  satisfying determinism end-to-end.
- **SC-004**: A short passphrase (< 12 bytes) and a wrong-length salt
  (≠ 16 bytes) each return a distinct named error in under 100
  milliseconds — i.e. the input validation runs before the multi-second
  Argon2id work.
- **SC-005**: AC-7 in the project acceptance matrix (Bitcoin crypto:
  BIP32 hierarchy) is marked green for the unit-test rows owned by this
  feature, with the test paths listed in the AC matrix existing and
  passing.
- **SC-006**: The fingerprint helper produces stable, 16-character
  hexadecimal output: in a 1,000-key sample, every fingerprint is
  exactly 16 hex characters long, every fingerprint is stable across
  re-derivations, and no two distinct keys collide.
- **SC-007**: An audit of the operator machine after running the full
  derivation flow shows zero new files containing key material — no
  master seed, no sub-key, no fingerprint cache. (Reinforces FR-010 in
  practice.)

## Assumptions

- The operator's passphrase is held in a secure source upstream of this
  feature (the macOS Keychain on the trusted host, per the project
  Security Requirements). This feature receives the passphrase as a byte
  sequence and does not concern itself with how it was retrieved.
- The salt is generated by an upstream component (the vault file format,
  defined in a separate feature) using a cryptographically secure random
  source. This feature treats the salt as a given input.
- The Argon2id parameters (`time = 4`, `memory = 256 MB`, `threads = 4`,
  `keyLen = 64`) are fixed by the project constitution and Security
  Requirements; they are not configurable inputs to this feature.
- The four hierarchical derivation paths (`m/44'/7743'/{0,1,2}'` and
  `m/44'/7743'/3'/{machine_index}`) are fixed by the project SPEC FR-3
  and may not be renumbered by this feature.
- The 12-byte minimum passphrase length is a deliberate floor consistent
  with the project's threat model; it is not configurable. Stronger
  enforcement (e.g. minimum-entropy estimation) is out of scope for
  this feature.
- The 16-byte salt length matches the salt slot in the vault file
  format. A different salt length would require coordinated changes to
  the vault format and is therefore rejected by this feature.
- This feature is a leaf concern — it neither calls into nor is called
  by any other hush internal component during derivation. Its outputs
  are passed by callers into other components; this feature is not
  responsible for their further handling.
- Memory hygiene of the derived material (zeroing, mlocking) is the
  responsibility of the consumer's secure-memory layer, defined in a
  separate feature. This feature returns derived material to the caller
  and does not persist or zero it after return.
- Concurrent invocations from independent goroutines are supported; this
  feature exposes no shared mutable state.
