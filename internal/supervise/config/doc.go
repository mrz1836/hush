// Package config owns the per-supervisor TOML configuration file: schema,
// defaults, validation, and path-safety checks. It is loaded once at supervisor
// startup (SDD-19..23, SDD-26..28) and produces a fully materialized,
// validated *Supervisor value with no secret material.
//
// Constitution principles in scope:
//   - IV   (TTL discipline + grace-window cap: cache_grace_ttl ≤ 4h, requested_ttl ≤ 24h)
//   - V    (operator visibility: validator allow-list explicit, unknown names rejected)
//   - VIII (TDD + 95% coverage + fuzz target #5 FuzzSuperviseTOML)
//   - IX   (no init(), no mutable globals, errors wrapped with %w, no goroutines)
//   - X    (no secrets in Supervisor struct; no os.Getenv for any field)
//   - XI   (zero new direct deps — reuses pelletier/go-toml/v2 from SDD-06)
//
// Locked exported API (SDD-18):
//   - type Supervisor, Child, DiscordRouting, Watchdog, Validator
//   - func Load(ctx context.Context, path string) (*Supervisor, error)
//   - func (s *Supervisor) Validate() error
//   - var Default*, Max* (defaults catalog + constitutional bounds)
//   - var Err* sentinel errors (one per documented rejection category)
//
// Constitution X: this package reads no environment variable for any
// supervisor field. The single env-reading call is os.UserHomeDir
// (for ~-expansion of non-secret path fields).
//
// See docs/CONFIG-SCHEMA.md "Supervisor config" section for the canonical
// schema, defaults, and validation rules this package enforces.
package config
