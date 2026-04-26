# Implementation Plan

This file converts the approved T-173 spec into a practical build sequence.

It is not the todo plan file.
It is the in-repo implementation roadmap an execution agent should follow once coding begins.

---

## Goal

Build hush in a sequence that preserves security clarity and minimizes rework.

The order matters.
If we implement child supervision before session policy is nailed down, or handlers before transport/auth primitives are stable, we create churn and subtle security bugs.

---

## Delivery principles

- build low-level trust primitives before orchestration layers
- validate each security claim with tests before stacking more behavior on top
- keep interactive and supervisor flows symmetrical where possible
- do not start convenience features before the daemon lifecycle is solid
- docs and code must stay aligned after every phase

---

## Phase order

## Phase 0 — documentation hardening

Purpose:
- eliminate ambiguity before implementation

Deliverables:
- core docs complete
- package map explicit
- config schema explicit
- lifecycle scenarios explicit
- testing strategy explicit
- build sequence explicit

Exit criteria:
- an implementation agent can build without inventing missing behavior

Status:
- in progress until all referenced docs exist and align

---

## Phase 1 — cryptographic and storage core

Purpose:
- establish the trust foundation

Core deliverables:
- Argon2id derivation
- BIP32 key hierarchy
- AES-256-GCM vault file format (`HUSH` magic, versioning, salt, nonce, ciphertext)
- secure bytes / zeroization model
- atomic vault write path
- startup file mode checks

Primary packages:
- `internal/keys`
- `internal/vault`
- `internal/logging` (redaction support)

Verification gates:
- deterministic tests for vault encode/decode round-trip
- negative tests for wrong passphrase / corrupted file / malformed header
- fuzz tests for vault decode path
- coverage target: 100% on crypto/storage core

Do not start Phase 2 until:
- vault format is stable
- derived-key paths are frozen in tests
- no key files are needed on disk

---

## Phase 2 — session and transport core

Purpose:
- make requests authentic and responses confidential

Core deliverables:
- ES256K JWT signing/validation
- interactive vs supervisor `session_type`
- TTL, scope, `client_ip`, `max_uses`, `jti` policy enforcement
- active/revoked token bookkeeping
- ECDSA request signing and verification
- nonce + timestamp replay protection
- ECIES secret response encryption/decryption helpers

Primary packages:
- `internal/token`
- `internal/transport`
- `internal/keys`

Verification gates:
- unit tests for all claim validation branches
- replay-attack tests
- token exhaustion tests
- wrong-IP rejection tests
- ECIES encrypt/decrypt round-trip + malformed ciphertext tests
- fuzz tests for JWT parse/validate and request verification inputs

Do not start Phase 3 until:
- request auth and token policy are stable
- plaintext secrets never appear in transport-layer tests/logging

---

## Phase 3 — server control plane

Purpose:
- expose the secured vault functionality over HTTP and Discord approval

Core deliverables:
- config load + validation
- server startup checks (Tailscale bind, NTP, file modes)
- `/claim`, `/s/<name>`, `/revoke/<jti>`, `/hz`
- Discord bot connection
- interactive approval flow and denial flow
- audit logging and optional audit-channel mirror
- atomic vault reload on SIGHUP

Primary packages:
- `internal/config`
- `internal/server`
- `internal/discord`
- `internal/logging`

Verification gates:
- handler tests for success and failure paths
- Discord-unavailable returns 503 test
- approval timeout/denial tests
- SIGHUP reload tests with old/new vault swap behavior
- audit-chain append tests

Do not start Phase 4 until:
- server can issue interactive sessions end-to-end
- interactive shell workflow is proven in integration tests

---

## Phase 4 — interactive CLI path

Purpose:
- make hush useful for human-driven shell sessions

Core deliverables:
- `hush request`
- `hush health`
- `hush revoke`
- `hush version`
- output modes (text/json/eval with explicit eval warning)
- shell wrapping path

Primary packages:
- `internal/cli`
- selected helper paths in `token`, `transport`, `server`

Verification gates:
- CLI flag validation tests
- exec/env injection integration tests
- `--format eval` explicit-gate tests
- usable human output for approval wait and error cases

Do not start Phase 5 until:
- interactive path is clean and predictable
- no file-based secret fallback exists anywhere in CLI flow

---

## Phase 5 — supervisor lifecycle

Purpose:
- solve the hard daemon problem for OpenClaw and Hermes

Core deliverables:
- `hush supervise`
- child lifecycle management
- silent refill across clean exit/crash within session TTL
- exit 78 stale-credential contract
- refresh window scheduler
- boot retry with backoff
- PID file + flock
- grace-window cache (optional)
- local Unix status socket
- `hush client status`
- `hush client refresh`

Primary packages:
- `internal/supervise`
- `internal/cli`
- `internal/config`

Verification gates:
- all lifecycle scenarios implemented in integration coverage
- child crash/restart tests
- session expiry tests
- vault restart / unknown-jti recovery tests
- duplicate supervisor start rejection test
- status socket schema tests

This is the highest-risk phase.
Do not compress it.

---

## Phase 6 — validator and alerting hardening

Purpose:
- make stale credentials visible before they become outages

Core deliverables:
- validator registry
- built-in validators: anthropic, anthropic-oauth, openai, google-ai, github
- alert rendering for validator failure, exit 78, log-pattern match
- alert rate limiting
- Discord disconnected / reconnected awareness

Primary packages:
- `internal/supervise`
- `internal/discord`

Verification gates:
- validator success/failure tests
- alert shape tests
- log-pattern watcher tests proving alert-only behavior
- refusal-to-start-child test on validator failure

---

## Phase 7 — deployment artifacts and clean-machine posture

Purpose:
- turn the product into a repeatable operational system

Core deliverables:
- `hush init`
- deploy examples/scripts for OpenClaw and Hermes
- launchd + systemd examples
- clean-machine checklist enforcement/docs
- client registration/bootstrap docs

Primary packages:
- `internal/cli`
- `deploy/`
- docs updates

Verification gates:
- bootstrap path tested on macOS and Linux where feasible
- deploy examples match config schema exactly
- docs and examples remain aligned

---

## Phase 8 — release hardening

Purpose:
- make the repo public-ready and production-ready

Core deliverables:
- coverage ≥ 90% overall
- fuzz suite green
- fortress / CI integration green
- polished README and public docs
- security review pass
- version/build metadata clean

Verification gates:
- `magex format:fix`
- `magex lint`
- `magex test:race`
- `go-pre-commit`
- release build via GoReleaser

---

## Cross-phase invariants

These must stay true throughout all phases:

- no secrets written to agent disk
- no key files written anywhere
- no auto-approve mode
- supervisor owns daemon auth continuity
- validators run on the supervisor, not the vault server
- log-pattern detection is alert-only
- all new behavior traces back to `docs/SPEC.md`

---

## Suggested file creation order

A practical first-pass creation sequence:

1. `internal/keys/*`
2. `internal/vault/*`
3. `internal/token/*`
4. `internal/transport/*`
5. `internal/config/*`
6. `internal/logging/*`
7. `internal/discord/*`
8. `internal/server/*`
9. `internal/cli/*`
10. `internal/supervise/*`
11. `deploy/*`

This order is not absolute, but it keeps dependency direction sane.

---

## What not to do

- do not build supervisor logic before token/session policy is stable
- do not wire provider validators into the vault server
- do not add convenience fallbacks that weaken fail-closed behavior
- do not let CLI UX decisions reshape security requirements
- do not treat Phase 0 docs as marketing copy; they are build inputs

---

## Phase 0 completion check

This file is sufficient when an implementation agent can answer:

- what phase comes next?
- what packages are touched in that phase?
- what tests gate moving on?
- what must not be started yet?

If that sequence is still fuzzy, Phase 0 is not done.
