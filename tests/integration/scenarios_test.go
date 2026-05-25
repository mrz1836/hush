//go:build integration

// scenarios_test.go is the index for the 17 Test_Scenario_NN_<slug>
// functions. Each scenario lives in its own scenario_NN_*_test.go file;
// this file carries only the sentinel-index reference and the one
// scenario (14) whose body is a thin wrapper over a shared helper.
//
// Sentinel index (SentinelSecret(N) per scenario):
//
//	N=1   Scenario 01  InteractiveShellRequest      scenario_01_test.go
//	N=2   Scenario 02  FirstDaemonBootstrap         scenario_02_test.go
//	N=3   Scenario 03  CleanChildExitRefill         scenario_03_test.go
//	N=4   Scenario 04  ChildCrashRefill             scenario_04_test.go
//	N=5   Scenario 05  ChildExit78Stale             scenario_05_test.go
//	N=6   Scenario 06  ValidatorFailure             scenario_06_test.go
//	N=7   Scenario 07  VaultRestartInvalidatesSession scenario_07_test.go
//	N=8   Scenario 08  DaytimeRefresh               scenario_08_test.go
//	N=9   Scenario 09a OvernightExpiry_Strict       scenario_09_test.go
//	N=10  Scenario 09b OvernightExpiry_Grace        scenario_09_test.go
//	N=11  Scenario 10  DiscordUnavailable           scenario_10_test.go
//	N=12  Scenario 11a TailscaleReady               scenario_11_tailscale_ready_test.go
//	N=13  Scenario 11b BootTimeout                  scenario_11_boot_timeout_test.go
//	N=14  Scenario 12  AgentStatusCheck             scenario_12_test.go
//	N=15  Scenario 13  MidSessionRotation           scenario_13_test.go
//	N=16  Scenario 14  DuplicateStart               scenario_14_test.go
//	N=17  Scenario 15  LogPatternMatch              scenario_15_test.go
//	N=18  Scenario 16  ReloadHTTPProxy              scenario_16_reload_test.go
//
// All 18 scenarios are wired against the real harness — none uses
// t.Skip; the suite reaches full coverage only when every scenario
// passes under `magex test:race` with the integration build tag.
package integration_test

import "testing"

// Test_Scenario_14_DuplicateStart is the duplicate-supervisor pidfile-
// collision flow per docs/LIFECYCLE-SCENARIOS.md §14.
func Test_Scenario_14_DuplicateStart(t *testing.T) {
	scenario14DuplicateStart(t)
}
