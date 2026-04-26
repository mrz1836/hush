# Testing Strategy

This file defines how hush proves its claims.

For a secrets broker, a security property does not count because it sounds right.
It counts when tests demonstrate it.

---

## Testing philosophy

hush is security-sensitive infrastructure.
That means:

- critical paths need extremely high coverage
- malformed input is first-class test material
- fuzzing is mandatory where parsers or crypto boundaries exist
- integration tests must exercise real lifecycle flows, not just happy-path units
- log and output assertions must prove secrets are not leaking accidentally

---

## Coverage targets

### Critical paths — 100%

These areas require complete branch coverage:

- Argon2id derivation wrappers
- BIP32 path derivation helpers
- vault file parse/write logic
- AES-GCM encrypt/decrypt boundaries
- ES256K JWT issue/validate logic
- ECIES encrypt/decrypt helpers
- request signature verification
- nonce/timestamp replay checks

### High-risk paths — 95%

- HTTP auth handlers
- token store and revocation
- supervisor state transitions
- validator orchestration
- SIGHUP vault reload path
- status socket output generation

### Operational paths — 85%

- Discord approval rendering and callback handling
- CLI flag validation and output adapters
- config parsing/defaulting/validation
- deployment/bootstrap helpers

### Low-risk paths — sensible coverage

- version output
- help text
- log formatting aesthetics

Overall repository minimum before public v0.1.0:
- 90%+

---

## Test layers

## 1. Unit tests

Use table-driven unit tests for all pure or mostly-pure logic.

Best candidates:
- config validation
- path normalization
- JWT claim validation
- request signing inputs
- replay window decisions
- validator response interpretation
- refresh-window calculations
- file mode checks

Guidelines:
- every branch should have a named case
- include malformed and boundary input, not just expected input
- prefer deterministic fixtures over random values except where randomness itself is being tested

---

## 2. Fuzz tests

Mandatory fuzz targets:

- vault file decode
- JWT parse/validate
- ECIES decrypt input handling
- request signature payload parsing
- supervisor config TOML parsing
- status socket JSON encoding if custom parsing is involved

Fuzz test goals:
- no panics
- no unbounded memory growth
- malformed input returns explicit errors
- no partial secret exposure in error messages

---

## 3. Integration tests

Integration tests prove components work together under realistic flows.

Required integration groups:

### Interactive session flow
- signed `/claim`
- Discord approval stub
- JWT issued
- secrets fetched via `/s/<name>`
- env injection into child shell/app

### Supervisor flow
- first daemon bootstrap
- silent refill on clean exit
- silent refill on crash within valid session
- exit 78 → awaiting approval
- refresh window prompt path
- status socket visibility
- duplicate supervisor start rejection

### Vault/server lifecycle
- atomic secret write
- SIGHUP reload swaps vault safely
- vault restart invalidates prior in-memory session state cleanly

### Failure paths
- Discord unavailable returns 503
- wrong IP rejected
- nonce replay rejected
- token exhausted rejected
- validator failure blocks child start
- watchdog alert fires but does not restart child automatically

---

## 4. Race and concurrency tests

Because hush handles sessions, reloads, and daemon restarts, concurrency matters.

Required areas:
- token use-count decrement under concurrent fetches
- active session revocation vs simultaneous fetch
- SIGHUP vault reload with in-flight requests
- supervisor child exit and refresh scheduler interactions
- audit log append ordering under concurrent events

These tests should run under `go test -race`.

---

## 5. Redaction and secrecy tests

We need tests that explicitly prove secrets do not leak.

Required assertions:
- logs do not contain plaintext secret values
- HTTP error bodies do not contain decrypted secret values
- audit events do not contain plaintext secret values
- CLI output does not print secrets unless explicitly using a dangerous format path
- supervisor alert messages identify scope names, not secret values

Suggested pattern:
- inject sentinel secret value like `SECRET_SHOULD_NEVER_APPEAR_123`
- run the path
- assert sentinel is absent from captured logs/output/errors

---

## 6. OS and environment coverage

Minimum matrix for meaningful confidence:

- macOS arm64
- Linux amd64

Critical OS-specific test targets:

### macOS
- Keychain item retrieval/integration seams
- launchd-oriented supervisor path assumptions
- socket path under `~/Library/Caches/hush`

### Linux
- `$XDG_RUNTIME_DIR` socket path handling
- systemd-oriented runtime assumptions
- permission and path handling differences

Where true OS integration is hard in CI, isolate the seam and unit test behavior around the abstraction.

---

## Lifecycle scenario test map

Each scenario in `docs/LIFECYCLE-SCENARIOS.md` should have at least one explicit integration test.

Required mapping:
- first interactive shell request
- first daemon bootstrap
- clean child exit within session
- crash within session
- child exit 78 stale path
- validator stale failure
- vault server restart / unknown-jti path
- daytime refresh window path
- overnight expiry with and without grace cache
- Discord unavailable path
- Tailscale boot retry path
- status query before agent work
- manual refresh after rotation
- duplicate supervisor start
- log-pattern alert-only behavior

If a scenario exists in docs but not in tests, the implementation is incomplete.

---

## Suggested test package layout

- `internal/keys/*_test.go`
- `internal/vault/*_test.go`
- `internal/token/*_test.go`
- `internal/transport/*_test.go`
- `internal/config/*_test.go`
- `internal/server/*_test.go`
- `internal/discord/*_test.go`
- `internal/supervise/*_test.go`
- `cmd/hush/*_test.go` only for CLI-specific behavior
- `test/integration/...` for multi-package flows if the repo adopts a shared integration test tree

---

## Required gates before public release

Before Z makes the repo public, all of these should pass:

- `magex format:fix`
- `magex lint`
- `magex test:race`
- `go-pre-commit`
- fuzz targets run clean
- coverage report shows 90%+ overall
- critical paths meet their higher thresholds

---

## Anti-patterns

Do not:

- count untested security claims as complete
- rely only on happy-path integration tests
- treat log redaction as obvious without explicit assertions
- skip race tests on stateful packages
- let provider validators hit expensive or write-capable endpoints in tests

---

## Phase 0 completion check

This file is sufficient when an implementation agent can answer:

- what needs 100% coverage?
- what must be fuzzed?
- what lifecycle flows require integration tests?
- how do we prove secrets are not leaking into logs/output?

If those answers are not explicit, Phase 0 is not done.
