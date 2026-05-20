//go:build integration

// scenarios_test.go houses the 17 Test_Scenario_NN_<slug> functions
// locked in spec FR-002. The names match verbatim — renaming any test
// requires a spec amendment first.
//
// Sentinel index (SentinelSecret(N) per scenario, FR-007 / FR-017):
//
//	N=1   Scenario 01  InteractiveShellRequest
//	N=2   Scenario 02  FirstDaemonBootstrap
//	N=3   Scenario 03  CleanChildExitRefill
//	N=4   Scenario 04  ChildCrashRefill
//	N=5   Scenario 05  ChildExit78Stale
//	N=6   Scenario 06  ValidatorFailure
//	N=7   Scenario 07  VaultRestartInvalidatesSession
//	N=8   Scenario 08  DaytimeRefresh
//	N=9   Scenario 09a OvernightExpiry_Strict
//	N=10  Scenario 09b OvernightExpiry_Grace
//	N=11  Scenario 10  DiscordUnavailable
//	N=12  Scenario 11a TailscaleReady
//	N=13  Scenario 11b BootTimeout
//	N=14  Scenario 12  AgentStatusCheck
//	N=15  Scenario 13  MidSessionRotation
//	N=16  Scenario 14  DuplicateStart
//	N=17  Scenario 15  LogPatternMatch
//
// Scenarios with harness wiring still pending in this SDD-25 chunk fail
// via scenarioPendingHarness — per spec FR-001 no scenario may t.Skip;
// failure is the operator-visible signal that AC-10's coverage path is
// incomplete and recurs on every suite run.
package integration_test

import "testing"

// Test_Scenario_01_InteractiveShellRequest is the interactive client
// flow per docs/LIFECYCLE-SCENARIOS.md §1. Implemented when the
// real-server claim → ECIES /s flow is wired (US1 / Phase 3a, T007).
func Test_Scenario_01_InteractiveShellRequest(t *testing.T) {
	scenarioPendingHarness(t, 1, "InteractiveShellRequest")
}

// Test_Scenario_02_FirstDaemonBootstrap is the supervisor first-boot
// flow per docs/LIFECYCLE-SCENARIOS.md §2.
func Test_Scenario_02_FirstDaemonBootstrap(t *testing.T) {
	scenarioPendingHarness(t, 2, "FirstDaemonBootstrap")
}

// Test_Scenario_03_CleanChildExitRefill is the clean-exit silent-refill
// flow per docs/LIFECYCLE-SCENARIOS.md §3.
func Test_Scenario_03_CleanChildExitRefill(t *testing.T) {
	scenarioPendingHarness(t, 3, "CleanChildExitRefill")
}

// Test_Scenario_04_ChildCrashRefill is the crash silent-refill flow.
func Test_Scenario_04_ChildCrashRefill(t *testing.T) {
	scenarioPendingHarness(t, 4, "ChildCrashRefill")
}

// Test_Scenario_05_ChildExit78Stale is the exit-78 stale-credential flow.
func Test_Scenario_05_ChildExit78Stale(t *testing.T) {
	scenarioPendingHarness(t, 5, "ChildExit78Stale")
}

// Test_Scenario_06_ValidatorFailure is the pre-start validator-block flow.
func Test_Scenario_06_ValidatorFailure(t *testing.T) {
	scenarioPendingHarness(t, 6, "ValidatorFailure")
}

// Test_Scenario_07_VaultRestartInvalidatesSession is the vault-restart 401-jti flow.
func Test_Scenario_07_VaultRestartInvalidatesSession(t *testing.T) {
	scenarioPendingHarness(t, 7, "VaultRestartInvalidatesSession")
}

// Test_Scenario_08_DaytimeRefresh is the daytime-refresh-window flow.
func Test_Scenario_08_DaytimeRefresh(t *testing.T) {
	scenarioPendingHarness(t, 8, "DaytimeRefresh")
}

// Test_Scenario_09_OvernightExpiry_Strict is the strict-mode overnight-
// expiry flow.
func Test_Scenario_09_OvernightExpiry_Strict(t *testing.T) {
	scenarioPendingHarness(t, 9, "OvernightExpiry_Strict")
}

// Test_Scenario_09_OvernightExpiry_Grace is the grace-cache overnight-
// expiry flow.
func Test_Scenario_09_OvernightExpiry_Grace(t *testing.T) {
	scenarioPendingHarness(t, 10, "OvernightExpiry_Grace")
}

// Test_Scenario_10_DiscordUnavailable is the Discord-503-fail-closed flow.
func Test_Scenario_10_DiscordUnavailable(t *testing.T) {
	scenarioPendingHarness(t, 11, "DiscordUnavailable")
}

// Test_Scenario_11_TailscaleReady is wired in scenario_11_tailscale_ready_test.go.
// Test_Scenario_11_BootTimeout    is wired in scenario_11_boot_timeout_test.go.
// Test_Scenario_12_AgentStatusCheck is wired in scenario_12_test.go.

// Test_Scenario_13_MidSessionRotation is the rotate-and-refresh flow.
func Test_Scenario_13_MidSessionRotation(t *testing.T) {
	scenarioPendingHarness(t, 15, "MidSessionRotation")
}

// Test_Scenario_14_DuplicateStart is the duplicate-supervisor pidfile-
// collision flow per docs/LIFECYCLE-SCENARIOS.md §14.
func Test_Scenario_14_DuplicateStart(t *testing.T) {
	scenario14DuplicateStart(t)
}

// Test_Scenario_15_LogPatternMatch is the watchdog-pattern-only-alert flow.
func Test_Scenario_15_LogPatternMatch(t *testing.T) {
	scenarioPendingHarness(t, 17, "LogPatternMatch")
}

// scenarioPendingHarness marks a scenario as not yet wired. The
// scenario suite reaches full coverage only when all 17 pass against a
// fully-wired harness; see docs/LIFECYCLE-SCENARIOS.md for the
// behavioral specs.
//
// The function uses t.Fatalf rather than t.Skip so an unimplemented
// scenario cannot silently report green — the failure is the
// operator-visible signal that harness work remains, and it will recur
// on every suite run until the scenario is implemented.
func scenarioPendingHarness(t *testing.T, sentinelN int, slug string) {
	t.Helper()
	t.Fatalf("scenario_%02d_%s not yet implemented (sentinel %d) — see docs/LIFECYCLE-SCENARIOS.md", sentinelN, slug, sentinelN)
}
