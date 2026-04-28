// Package testutil is the project's shared, test-only harness package.
// It must never be imported by production code (see Constitution Principles I and IX).
//
// Exported helpers:
//   - NewTestVault  — writes a real HUSH-format vault inside t.TempDir()
//   - NewTestKeys   — returns a deterministic 64-byte master seed
//   - SentinelSecret — returns the canonical SECRET_SHOULD_NEVER_APPEAR_<n> marker
//   - AssertSentinelAbsent — fails the test if the sentinel appears in the haystack
//   - DiscordStub   — programmable Discord approval stub; satisfies Approver
//   - NewDiscordStub — constructs a DiscordStub and registers t.Cleanup
//   - ApprovalCall  — one recorded call entry on DiscordStub
//   - Approver      — minimal interface; widened by SDD-11 in internal/discord
package testutil
