package main

import "testing"

// TestExampleBuilds is a compile-only smoke test. If the example
// drifts out of sync with the public pkg/client API the build will
// break here before CI ever runs the demo program.
func TestExampleBuilds(t *testing.T) {
	t.Helper()
	// Nothing to assert — the package compiling is the test.
}
