package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mrz1836/hush/internal/keychain"
)

type repairableACLKeychain struct {
	*aclKeychain

	repairErr   error
	repairCalls int
}

func (r *repairableACLKeychain) RepairACL(context.Context, string, string) error {
	r.mu.Lock()
	r.repairCalls++
	if r.repairErr != nil {
		err := r.repairErr
		r.mu.Unlock()
		return err
	}
	r.denyRemaining = 0
	r.mu.Unlock()
	return nil
}

func newKeychainCmdDepsForTest(kc keychain.Keychain) *keychainCmdDeps {
	return &keychainCmdDeps{
		keychain:   kc,
		binaryPath: func() (string, error) { return testInitBinaryPath, nil },
		repairACL:  defaultKeychainACLRepair,
	}
}

func TestNewKeychainCmd_HasSubcommands(t *testing.T) {
	t.Parallel()
	cmd := newKeychainCmd()
	subs := map[string]bool{}
	for _, c := range cmd.Commands() {
		subs[c.Use] = true
	}
	require.True(t, subs["doctor"])
	require.True(t, subs["repair"])
}

func TestRunKeychainDoctor_OK(t *testing.T) {
	t.Parallel()
	fx, _ := newACLFixture(t, 0)
	deps := newKeychainCmdDepsForTest(fx.deps.keychain)
	buf := &strings.Builder{}
	stderr := newStream(buf, false, true)

	err := runKeychainDoctor(context.Background(), nil, stderr, deps)
	require.NoError(t, err)
	require.Contains(t, buf.String(), "OK")
	require.Contains(t, buf.String(), testInitBinaryPath)
}

func TestRunKeychainDoctor_Missing(t *testing.T) {
	t.Parallel()
	fx := newInitFixture(t)
	deps := newKeychainCmdDepsForTest(fx.deps.keychain)
	buf := &strings.Builder{}
	stderr := newStream(buf, false, true)

	err := runKeychainDoctor(context.Background(), nil, stderr, deps)
	require.ErrorIs(t, err, errKeychainDoctorMissing)
	require.Contains(t, buf.String(), "missing")
}

func TestRunKeychainDoctor_Denied(t *testing.T) {
	t.Parallel()
	_, acl := newACLFixture(t, 2)
	deps := newKeychainCmdDepsForTest(acl)
	buf := &strings.Builder{}
	stderr := newStream(buf, false, true)

	err := runKeychainDoctor(context.Background(), nil, stderr, deps)
	require.ErrorIs(t, err, errKeychainDoctorDenied)
	require.Contains(t, buf.String(), "denied")
	require.NotContains(t, buf.String(), testBotTokenInput)
}

func TestRunKeychainRepair_Success(t *testing.T) {
	t.Parallel()
	_, acl := newACLFixture(t, 1)
	repairable := &repairableACLKeychain{aclKeychain: acl}
	deps := newKeychainCmdDepsForTest(repairable)
	buf := &strings.Builder{}
	stderr := newStream(buf, false, true)

	err := runKeychainRepair(context.Background(), nil, stderr, deps)
	require.NoError(t, err)
	require.Equal(t, 1, repairable.repairCalls)
	require.Contains(t, buf.String(), "repaired")
	require.NotContains(t, buf.String(), testBotTokenInput)
}

func TestRunKeychainRepair_Missing(t *testing.T) {
	t.Parallel()
	fx := newInitFixture(t)
	deps := newKeychainCmdDepsForTest(fx.deps.keychain)
	buf := &strings.Builder{}
	stderr := newStream(buf, false, true)

	err := runKeychainRepair(context.Background(), nil, stderr, deps)
	require.ErrorIs(t, err, errKeychainDoctorMissing)
	require.Contains(t, buf.String(), "missing")
}

func TestRunKeychainRepair_StillDenied(t *testing.T) {
	t.Parallel()
	_, acl := newACLFixture(t, 2)
	repairable := &repairableACLKeychain{aclKeychain: acl}
	// Keep denying after repair to force the re-check failure.
	deps := newKeychainCmdDepsForTest(repairable)
	errBuf := &strings.Builder{}
	stderr := newStream(errBuf, false, true)

	// Force the repair helper to be a no-op so the retry still denies.
	deps.repairACL = func(context.Context, keychain.Keychain, string, string) error { return nil }

	err := runKeychainRepair(context.Background(), nil, stderr, deps)
	require.ErrorIs(t, err, errKeychainDoctorDenied)
	require.Contains(t, errBuf.String(), "still denied")
}

func TestKeychainCmd_NoSecretLeak(t *testing.T) {
	t.Parallel()
	_, acl := newACLFixture(t, 1)
	repairable := &repairableACLKeychain{aclKeychain: acl}
	deps := newKeychainCmdDepsForTest(repairable)

	buf := &strings.Builder{}
	stderr := newStream(buf, false, true)
	_ = runKeychainDoctor(context.Background(), nil, stderr, deps)
	_ = runKeychainRepair(context.Background(), nil, stderr, deps)
	require.NotContains(t, buf.String(), "preexisting-bot-token")
	require.NotContains(t, buf.String(), testBotTokenInput)
}
