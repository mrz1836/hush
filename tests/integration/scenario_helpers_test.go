//go:build integration

// scenario_helpers_test.go holds small shared fixtures used by more than one
// Test_Scenario_* body: a pointer helper for the optional cache-mode override
// and a deterministic failing Validator for the validator-block scenario.
package integration_test

import (
	"context"
	"errors"
	"fmt"

	"github.com/mrz1836/hush/internal/vault/securebytes"
)

// boolPtr returns a pointer to b — used for SupervisorOpts.CacheSecretsForRestart.
func boolPtr(b bool) *bool { return &b }

// errSyntheticValidator is the deterministic rejection a failingValidator
// returns. It carries no secret material.
var errSyntheticValidator = errors.New("integration: synthetic validator rejection (401)")

// failingValidator is a supervise.Validator that always rejects, naming the
// scope but never the secret value (Constitution X).
type failingValidator struct{}

// Validate always returns a scope-named wrapped rejection.
func (failingValidator) Validate(_ context.Context, scope string, _ *securebytes.SecureBytes) error {
	return fmt.Errorf("integration: scope %q: %w", scope, errSyntheticValidator)
}
