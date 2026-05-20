//go:build integration

// Package harness is the lifecycle-scenario integration test
// fixture toolkit. It composes the real internal/* packages
// (supervise.Lifecycle, server.Server, audit.Writer, token.Store,
// vault, transport/ecies, transport/sign, keys) end-to-end with only
// four boundaries mocked: Discord, the five provider validator HTTP
// endpoints, the wall clock, and the Tailscale-reachability probe.
//
// The 6-file inventory is:
//
//	vault.go        — real internal/vault fixture + sentinel injection
//	server.go       — real internal/server in-process + validator stubs
//	discord.go      — DiscordStub wrapper + connectivity sequence
//	supervisor.go   — real *supervise.Lifecycle composition
//	child.go        — programmable scripted child via os.Executable()
//	log_capture.go  — slog sink + cross-stream AssertSentinelAbsent
//
// All harness code carries the //go:build integration build tag.
// Default `go test ./tests/integration/...` (no -tags) compiles zero
// files and exits with "no Go files".
//
// The package is consumed ONLY by test files under tests/integration/.
// A depguard rule in .golangci.yml forbids any production file from
// importing this package.
package harness
