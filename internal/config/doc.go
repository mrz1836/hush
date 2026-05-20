// Package config owns the server-side TOML configuration file: schema,
// defaults, validation, and path-safety checks. It is loaded once at server
// startup and at hush init. Typed sentinel errors guide
// operators to a working config without ever crashing on bad input.
//
// Constitution principles in scope:
//   - III  (Argon2id minimums — argon_memory_mb ≥ 256, time ≥ 4, threads ≥ 4)
//   - VI   (Tailscale-only bind — listen_addr must be in 100.64.0.0/10)
//   - VIII (TDD + fuzz target #5 FuzzServerTOML, coverage ≥ 95%)
//   - IX   (no init(), no mutable globals, errors wrapped with %w)
//   - X    (no secrets in Server struct; no os.Getenv for secret fields)
//   - XI   (one new direct dep: github.com/pelletier/go-toml/v2)
//
// Locked exported API:
//   - type Server, ServerSection, DiscordSection, CryptoSection, NetworkSection, SecuritySection
//   - func LoadServer(ctx context.Context, path string) (*Server, error)
//   - func (s *Server) Validate() error
//   - var Default*, Min*, Max*, TailscaleCGNAT
//   - var Err* sentinel errors
//
// Constitution X: this package reads no environment variable for any
// secret-bearing field. The single env-reading call is os.UserHomeDir
// (for ~-expansion of non-secret path fields).
package config
