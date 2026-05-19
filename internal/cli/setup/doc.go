// Package setup hosts the diagnostic-first foundations the guided
// `hush init server` flow depends on: a structured error taxonomy
// (errors.go), a preflight check registry (preflight.go), and an
// existing-state classifier (state.go) that labels pre-existing
// config / vault / state-dir / Keychain artifacts as safe-to-reuse,
// repairable, or collision.
//
// The package is intentionally free of any CLI rendering, prompt, or
// audit-writer dependency so a future `hush doctor` subcommand can
// reuse the same registry and taxonomy without refactoring (Plan A3).
//
// Constitutional principles in scope:
//   - VII (CLI design standards): every user-facing failure routes
//     through a typed error with a one-line remedy hint suitable for
//     copy/paste — no anonymous fmt.Errorf strings on the diagnostic
//     surface.
//   - IX (idiomatic Go): no init(); no globals beyond sentinel errors;
//     ctx-first; tests use deterministic clocks.
//   - X (observability & redaction): no Keychain item value, bot token
//     byte, or vault byte ever travels through this package. Detail
//     and remedy hints carry only paths, exit codes, and category
//     names.
//
// Exported surface:
//
//   - [Status], [SetupCheckResult], [Check], [Registry] — the
//     diagnostic primitive used by every preflight step.
//   - [Classification], [Artifact], [Classifier] — the per-artifact
//     existing-state inspector.
//   - [Archive] — atomic move-to-`.bak-<RFC3339>` helper.
//   - [Err*] sentinel errors with a [RemedyHint] method per type.
//
// The guided setup flow wires these types into `internal/cli/init.go`
// and adds platform implementations of each Check.
package setup
