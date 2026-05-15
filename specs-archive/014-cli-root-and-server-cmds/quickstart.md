# Quickstart — SDD-14

**Branch**: `014-cli-root-and-server-cmds` | **Date**: 2026-05-01

How an implementer (human or AI) drives each subcommand locally during
development, and how the integration test exercises the end-to-end
`serve` lifecycle. This document is consumed by `/speckit-tasks` to
generate execution steps.

---

## 0. Prerequisites

- Go 1.26.1 (per `go.mod`).
- `magex` build tool installed (`go install github.com/mrz1836/magex/cmd/magex@latest` if missing).
- Tailscale running on the host (for `serve` end-to-end; the chassis's
  startup checks refuse a non-Tailscale interface unless the test
  config disables `RequireTailscale`).
- For `serve` only: a populated keychain item `hush-discord` containing
  the bot token (or skip — the integration test stubs the approver and
  never hits the keychain).

---

## 1. Building the binary

```bash
# Dev build — Version/Commit/Date placeholders default to "dev"/"unknown".
go build -o ./hush ./cmd/hush

# Release-shaped build with metadata injection.
go build \
  -ldflags "-X github.com/mrz1836/hush/internal/cli.Version=$(git describe --tags --always) \
            -X github.com/mrz1836/hush/internal/cli.Commit=$(git rev-parse --short HEAD) \
            -X github.com/mrz1836/hush/internal/cli.Date=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  -o ./hush ./cmd/hush
```

Verify injection landed:

```bash
./hush version            # TTY: human lines
./hush version | cat      # non-TTY: JSON
```

---

## 2. Driving `hush version`

No prerequisites beyond the built binary.

```bash
./hush version
# hush version v0.1.0
# commit:  fb3e402
# built:   2026-05-01T12:34:56Z

./hush version | jq .
# {"version":"v0.1.0","commit":"fb3e402","date":"2026-05-01T12:34:56Z"}

./hush version --no-color    # no ANSI on success-marker (irrelevant for version)
echo $?                       # 0
```

---

## 3. Driving `hush health`

Requires a running `hush serve` (see §5) or any HTTP responder at the
configured address.

```bash
# Happy path against a running server.
./hush health
# ✔ status              ok
# ✔ uptime              12m34s
#   secrets_count       7
#   active_tokens       2
# ✔ discord_connected   true
# ✔ config_valid        true
# ✔ vault_loaded        true
# ✔ clock_in_sync       true
echo $?                       # 0

# Non-TTY → JSON.
./hush health | jq .
# {"status":"ok",...}
echo $?                       # 0

# Server down.
./hush health
# could not connect to hush server at http://100.x.y.z:7743/h/abcdef/hz: connection refused
echo $?                       # 1

# Partial-health (e.g. discord_connected=false).
./hush health
# ✔ status              ok
# ✔ uptime              12m34s
#   secrets_count       7
#   active_tokens       2
# ✘ discord_connected   false
# ✔ config_valid        true
# ✔ vault_loaded        true
# ✔ clock_in_sync       true
echo $?                       # 1   (FR-017a)
```

---

## 4. Driving `hush revoke`

Requires a running `hush serve` and an active token id.

```bash
# Pipe the passphrase (CI / launchd style).
echo -n "$VAULT_PASSPHRASE" | ./hush revoke \
  --server http://100.x.y.z:7743 \
  --jti 8f3a2c1e-9d4b-4f0a-b6e8-2d5e6c7f8a9b
# revoked jti=8f3a2c1e-9d4b-4f0a-b6e8-2d5e6c7f8a9b
echo $?                       # 0

# Interactive (TTY prompt for passphrase).
./hush revoke --server http://100.x.y.z:7743 --jti 8f3a2c1e-...
# Vault passphrase: <typed without echo>
# revoked jti=8f3a2c1e-...

# Missing flag.
./hush revoke --server http://100.x.y.z:7743
# missing required flag: --jti
echo $?                       # 2

# Server says signature invalid.
./hush revoke --server http://100.x.y.z:7743 --jti 8f3a2c1e-...
# server rejected revocation: signature invalid (or jti unknown — server treats both alike)
echo $?                       # 3
```

---

## 5. Driving `hush serve`

Requires `~/.hush/config.toml` (see `docs/CONFIG-SCHEMA.md` for the
full schema), `~/.hush/secrets.vault` (created by `hush init` — owned
by SDD-15), and a Discord bot token in the keychain.

```bash
# CI / launchd style — passphrase from a pipe.
cat ~/.hush/.passphrase-ephemeral | ./hush serve
# (Stays running; SIGTERM to stop.)

# Interactive — passphrase typed at the prompt.
./hush serve
# Vault passphrase: <typed without echo>
# (Stays running.)

# No passphrase source (no pipe, no TTY).
./hush serve < /dev/null
# no passphrase source: stdin is not a pipe and is not a terminal
echo $?                       # 2

# Verbose tracing (stderr lines).
./hush serve --verbose
# config: loaded /home/op/.hush/config.toml
# stdin: pipe detected
# keys: master seed derived
# audit: writer started at /home/op/.hush/audit.jsonl
# discord: bot connecting to channel <id>
# server: listening on 100.x.y.z:7743
# server: ready
```

To stop:

```bash
# In another shell:
kill -TERM $(pgrep -f "hush serve")
# The process logs "server: shutting down", drains in-flight requests,
# and exits 0.
```

---

## 6. Running the tests

```bash
# All unit tests (race-clean).
magex test:race

# Coverage for internal/cli specifically.
go test -cover ./internal/cli/

# Integration test (TestServe_StartAndShutdown).
magex test:race -tags=integration

# The static "no Getenv on the passphrase path" test runs as part
# of magex test:race; no separate invocation needed.
```

Coverage gate: ≥ 85% on `internal/cli`. The Implement phase asserts
this via `go test -cover ./internal/cli/` and refuses to commit if
the value falls below 85.

---

## 7. Smoke-checking each subcommand's `--help`

```bash
./hush --help
# Usage:
#   hush [command]
#
# Available Commands:
#   health      Check the health of the hush server
#   revoke      Revoke an active session token
#   serve       Start the hush vault server
#   version     Print build version
#
# Flags:
#   -c, --config string   Path to configuration file (default "~/.hush/config.toml")
#       --no-color        Force no ANSI color in output
#   -q, --quiet           Suppress all non-error output
#   -v, --verbose         Add stderr trace of resolved config + step transitions

./hush serve --help
./hush health --help
./hush version --help
./hush revoke --help
```

Each per-subcommand `--help` documents its specific flags and prints
the full exit-code table for that subcommand.

---

## 8. Verifying the public-contract guarantees

```bash
# (a) No viper anywhere.
grep -RnE '"github.com/spf13/viper"' cmd/ internal/ && echo "FAIL" || echo "OK"

# (b) No os.Getenv anywhere on the passphrase path.
grep -RnE 'os\.Getenv' internal/cli/ && echo "FAIL" || echo "OK"

# (c) No SECRET sentinel in any test artifact (post-test-run).
grep -Rn "SECRET_SHOULD_NEVER_APPEAR_14" \
  $(find . -name "*.go" -not -path "*/vendor/*") || echo "no in-source matches (good)"
# The sentinel SHOULD appear in test files (planted) but NEVER in
# captured output — the tests assert this themselves.

# (d) Exit codes round-trip.
./hush version; echo $?       # 0
./hush health  --server http://127.0.0.1:1; echo $?    # 1 (refused)
./hush revoke  --jti deadbeef; echo $?                  # 2 (no --server)
```

---

## 9. CI gates (Implement-phase verification)

The Implement phase runs all of:

```bash
magex format:fix
magex lint
magex test:race
magex test:race -tags=integration
go test -cover ./internal/cli/                  # ≥ 85%
./hush --help                                    # all four subcommands listed
./hush serve --help && ./hush health --help && \
./hush version --help && ./hush revoke --help

# Build version injection works.
go build -ldflags "-X github.com/mrz1836/hush/internal/cli.Version=v0.1.0-test" \
  -o ./hush ./cmd/hush
./hush version | grep "v0.1.0-test"

# os.Getenv ban (manual CI grep).
grep -RnE 'os\.Getenv' internal/cli/ && exit 1 || true

# govulncheck on the new tree.
govulncheck ./...
```

Only after all gates pass does the chunk make its single combined
commit (per SDD-14 Prompt 5 step 9).
