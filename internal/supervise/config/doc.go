// Package config owns the per-supervisor TOML configuration file: schema,
// defaults, validation, and path-safety checks. It is loaded once at supervisor
// startup and produces a fully materialized, validated *Supervisor value
// with no secret material.
//
// Exported API:
//   - type Supervisor, Child, DiscordRouting, Watchdog, Validator
//   - func Load(ctx context.Context, path string) (*Supervisor, error)
//   - func (s *Supervisor) Validate() error
//   - var Default*, Max* (defaults catalog + bounds)
//   - var Err* sentinel errors (one per documented rejection category)
//
// This package reads no environment variable for any supervisor field.
// The single env-reading call is os.UserHomeDir (for ~-expansion of
// non-secret path fields).
//
// See docs/CONFIG-SCHEMA.md "Supervisor config" section for the canonical
// schema, defaults, and validation rules this package enforces.
package config
