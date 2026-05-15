# Quickstart: `internal/logging`

**Audience**: every implementation agent and every internal-package author after SDD-05 lands.
**Last updated**: 2026-04-27 (Phase 1 of SDD-05)

This is the operational cheat-sheet for using the project-wide logger. The package's contract and rationale live in [api.md](./contracts/api.md) and [data-model.md](./data-model.md); this file shows you how to get a logger, how to log a secret-bearing struct, and how to verify the redaction guarantees in your own tests.

---

## 1. Get a logger

```go
import (
    "log/slog"
    "github.com/mrz1836/hush/internal/logging"
)

// Auto-detect format and level from the environment defaults.
log := logging.New(logging.Options{})
log.Info("server starting", "addr", "100.97.178.13:7743")
```

What the zero `Options{}` does:
- `Level` zero → `slog.LevelInfo`. DEBUG is dropped; INFO/WARN/ERROR pass.
- `Format` zero → `FormatAuto`. Writes JSON unless `Out` is a `*os.File` that is a TTY.
- `Out` nil → `os.Stderr`.

---

## 2. Configure explicitly

```go
log := logging.New(logging.Options{
    Level:  slog.LevelDebug,        // verbose — for triage
    Format: logging.FormatJSON,     // force JSON regardless of destination
    Out:    os.Stderr,              // explicit (also the default)
})
```

For interactive CLI commands that want guaranteed text:

```go
log := logging.New(logging.Options{Format: logging.FormatText})
```

---

## 3. Log a secret-bearing value safely

The Layer 5 secure-container (SDD-02 `securebytes.SecureBytes`) implements `slog.LogValuer`. Pass it directly as a value:

```go
import "github.com/mrz1836/hush/internal/vault/securebytes"

sb, _ := securebytes.New([]byte("sk-ant-this-must-not-leak"))
defer sb.Destroy()

log.Info("loaded secret", "name", "ANTHROPIC_API_KEY", "value", sb)
// Captured: ... value=[redacted] ...
```

You do not need to remember to call `LogValue()`. The handler chain calls `Value.Resolve()` on every attribute before rendering, so `SecureBytes` always renders as `[redacted]`. This works at every nesting depth — including `slog.Group`.

---

## 4. The regex backstop catches mistakes the type system cannot

If a caller builds a free-form string that embeds an upstream credential pattern, the regex backstop redacts it before the bytes leave the writer:

```go
log.Warn("downstream returned a key in the response body: sk-ant-fake-leakage")
// Captured: ... msg="downstream returned a key in the response body: [redacted]" ...
```

Currently shipped patterns (per `docs/SECURITY.md` §1.1):

| Class                | Prefix / shape         |
|----------------------|------------------------|
| Anthropic API key    | `sk-ant-...`           |
| OpenAI project key   | `sk-proj-...`          |
| GitHub PAT           | `ghp_...`              |
| AWS access key       | `AKIA[A-Z0-9]{16}`     |

Adding a pattern is a source change in `redact_patterns.go` — and per `docs/SECURITY.md` policy, the pattern must land in `docs/SECURITY.md` §1.1 first.

---

## 5. Test redaction in your own package

```go
func TestMyHandler_DoesNotLeakAPIKey(t *testing.T) {
    var buf bytes.Buffer
    log := logging.New(logging.Options{Format: logging.FormatJSON, Out: &buf})

    sentinel := []byte("SECRET_SHOULD_NEVER_APPEAR_TEST")
    sb, err := securebytes.New(append([]byte(nil), sentinel...))
    require.NoError(t, err)
    defer sb.Destroy()

    log.Info("handling request", "value", sb)

    require.NotContains(t, buf.String(), string(sentinel))
    require.Contains(t, buf.String(), "[redacted]")
}
```

This is the pattern every downstream package SHOULD follow when it has a code path that touches secret material — small, fast, and asserts the load-bearing invariant directly.

---

## 6. What you MUST NOT do

- Do not call `slog.SetDefault(log)`. The package's contract is "no mutation of `slog.Default`"; if you set the default to one of these loggers, every package that obtains the default observes redaction policy changes through global state. Thread the logger explicitly.
- Do not type-assert and unwrap the inner handler. The wrapper handler is private; reaching into it via `reflect` or unsafe casting voids the redaction guarantee.
- Do not concatenate a secret bytes slice into a string before passing to the logger — the type-driven rail loses its grip the moment the secret becomes a `string`. Wrap with `SecureBytes` and pass the wrapper directly.
- Do not rotate logs, file-roll, or buffer in this layer. The logger writes to its `Options.Out`. Rotation is the supervisor's concern (launchd / systemd / journald).
- Do not use this logger for audit events. Audit emission has its own ECDSA-signed, hash-chained channel (Layer 6, future chunk). Operational logs and audit logs are deliberately separate per Constitution X.

---

## 7. Deferring to the constitution

If a question is not answered above, the answers in priority order are:
1. `.specify/memory/constitution.md` Principles IX, X, XI.
2. `docs/SECURITY.md` §1.1 (credential classes).
3. `docs/OPERATIONS.md` (logging tier definitions).
4. `docs/sdd/SDD-05.md` (the chunk contract).
5. The contract document at `specs/005-logging/contracts/api.md`.

All of the above are checked-in source-of-truth — when they conflict, the constitution wins, and any drift between the others is an issue to file before writing code.
