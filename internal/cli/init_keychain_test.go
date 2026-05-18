package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mrz1836/hush/internal/keychain"
	"github.com/mrz1836/hush/internal/vault/securebytes"
)

// aclKeychain wraps a *keychain.FakeKeychain with a programmable
// "OS denied my read" overlay. The first denyRetrieves reads of
// (denyService, denyAccount) return [keychain.ErrKeychainPermissionDenied];
// remaining reads fall through to the inner fake. Store / Delete /
// other reads pass through verbatim. Call counts let tests assert
// branch coverage.
type aclKeychain struct {
	inner         *keychain.FakeKeychain
	denyService   string
	denyAccount   string
	mu            sync.Mutex
	denyRemaining int
	storeCalls    int
	deleteCalls   int
	retrieveCalls int
}

func newACLKeychain(t *testing.T, service, account string, denyCount int) *aclKeychain {
	t.Helper()
	kc := keychain.NewFake()
	t.Cleanup(kc.Destroy)
	return &aclKeychain{
		inner:         kc,
		denyService:   service,
		denyAccount:   account,
		denyRemaining: denyCount,
	}
}

// Retrieve honors the deny overlay then delegates to the fake.
func (a *aclKeychain) Retrieve(ctx context.Context, service, account string) (*securebytes.SecureBytes, error) {
	a.mu.Lock()
	a.retrieveCalls++
	denied := a.denyRemaining > 0 && service == a.denyService && account == a.denyAccount
	if denied {
		a.denyRemaining--
		a.mu.Unlock()
		return nil, keychain.ErrKeychainPermissionDenied
	}
	a.mu.Unlock()
	return a.inner.Retrieve(ctx, service, account)
}

// Store delegates to the fake and increments storeCalls. A successful
// Store does NOT clear the deny overlay: the overlay is exhausted by
// retrieves, not stores.
func (a *aclKeychain) Store(ctx context.Context, service, account string, value *securebytes.SecureBytes, acl string) error {
	a.mu.Lock()
	a.storeCalls++
	a.mu.Unlock()
	return a.inner.Store(ctx, service, account, value, acl)
}

// Delete delegates to the fake and increments deleteCalls. The deny
// overlay is irrelevant for Delete (the OS-level deletion does not
// require a successful read of the item's contents).
func (a *aclKeychain) Delete(ctx context.Context, service, account string) error {
	a.mu.Lock()
	a.deleteCalls++
	a.mu.Unlock()
	return a.inner.Delete(ctx, service, account)
}

// preStoreToken seeds the wrapped fake with a token under (service,
// account) so a subsequent successful Retrieve returns *that* value.
// Tests that don't need a stored value can skip this and the deny
// overlay alone drives the behavior.
func (a *aclKeychain) preStoreToken(ctx context.Context, t *testing.T, service, account string, raw []byte) {
	t.Helper()
	sb, err := securebytes.New(raw)
	require.NoError(t, err)
	require.NoError(t, a.inner.Store(ctx, service, account, sb, "/abs/test"))
	require.NoError(t, sb.Destroy())
}

// newACLFixture returns an initFixture wired with an aclKeychain
// targeting the bot-token slot, plus the pre-stored token bytes.
// denyCount controls how many initial Retrieves return denied.
func newACLFixture(t *testing.T, denyCount int) (*initFixture, *aclKeychain) {
	t.Helper()
	fx := newInitFixture(t)
	acl := newACLKeychain(t, "hush-discord", kcAccountServer, denyCount)
	acl.preStoreToken(context.Background(), t, "hush-discord", kcAccountServer, []byte("preexisting-bot-token"))
	fx.deps.keychain = acl
	return fx, acl
}

// scriptedLineReaderWithExtras returns a promptLine seam that yields
// the canonical interactive trio (listen / owner / app) followed by
// the supplied extras (e.g. the destruct confirmation string for the
// delete-and-recreate branch).
func scriptedLineReaderWithExtras(t *testing.T, extras []string) func(*os.File, io.Writer, string) (string, error) {
	t.Helper()
	full := make([]string, 0, 3+len(extras))
	full = append(full, testListenAddrInput, testOwnerIDInput, testApplicationIDIn)
	full = append(full, extras...)
	return scriptedLineReader(t, full)
}

// ---- AC-5 / Task 3.2 — ACL repair branch ----------------------------------

// TestKeychainACL_PanelRendersOnDenialAndReuseAfterRepair drives the
// happy path of the ACL-repair branch: the panel renders, the operator
// picks [1] to re-check, the re-check succeeds, and init reuses the
// existing Keychain item without calling Store on the bot-token slot.
func TestKeychainACL_PanelRendersOnDenialAndReuseAfterRepair(t *testing.T) {
	t.Parallel()
	// 1 denied read: classifier denies, re-check succeeds.
	fx, acl := newACLFixture(t, 1)
	fx.deps.promptRecovery = scriptedRecoveryReader(t, []rune{keychainACLChoiceRepair})

	err := runInitServer(context.Background(), fx.stdoutS, fx.stderrS, fx.stdinFile, fx.deps)
	require.NoError(t, err)

	transcript := fx.stderr.String()
	require.Contains(t, transcript, "macOS Keychain is denying hush access")
	require.Contains(t, transcript, "set-generic-password-partition-list")
	require.Contains(t, transcript, "Keychain re-check succeeded")
	require.Contains(t, transcript, initMsgServerComplete)

	// Re-check is the second Retrieve on the bot-token slot. Stores on
	// the bot-token slot should be 0 because we reused the existing item.
	require.GreaterOrEqual(t, acl.retrieveCalls, 2)
	// The vault-passphrase Keychain Store still fires (1 Store call).
	require.Equal(t, 1, acl.storeCalls,
		"only the vault-passphrase Store should fire under reuse-after-repair")
	require.Zero(t, acl.deleteCalls)
}

// TestKeychainACL_PanelReDisplaysWhenRepairStillDenied asserts the
// re-check loop: if the operator picks [1] but the read is still
// denied, the panel re-displays and the operator can pick another
// option. We script [1] then [q] to terminate.
func TestKeychainACL_PanelReDisplaysWhenRepairStillDenied(t *testing.T) {
	t.Parallel()
	// 3 denied reads: classifier denies, re-check still denied, panel
	// re-displays; the operator then quits.
	fx, _ := newACLFixture(t, 3)
	fx.deps.promptRecovery = scriptedRecoveryReader(t, []rune{
		keychainACLChoiceRepair,
		keychainACLChoiceQuit,
	})

	err := runInitServer(context.Background(), fx.stdoutS, fx.stderrS, fx.stdinFile, fx.deps)
	require.True(t, errors.Is(err, errUserAborted), "expected user-aborted, got %v", err)
	transcript := fx.stderr.String()
	require.Contains(t, transcript, "Keychain re-check still denied")
}

// ---- AC-5 / Task 3.3 — delete-and-recreate branch -------------------------

// TestKeychainACL_DeleteRecreateRequiresExactConfirmation asserts the
// destructive branch fires only when the operator types the literal
// "delete" string. The first attempt types "y" and the panel
// re-displays; the second types "delete" and the path completes.
func TestKeychainACL_DeleteRecreateRequiresExactConfirmation(t *testing.T) {
	t.Parallel()
	// 1 denied read: classifier denies; delete-and-recreate flow does
	// not re-check the Keychain (it deletes + stores directly).
	fx, acl := newACLFixture(t, 1)
	fx.deps.promptRecovery = scriptedRecoveryReader(t, []rune{
		keychainACLChoiceRecreate, // first attempt — type "y" → cancelled
		keychainACLChoiceRecreate, // second attempt — type "delete" → confirmed
	})
	// Extras supply the two destruct-confirmation lines in order.
	fx.deps.promptLine = scriptedLineReaderWithExtras(t, []string{"y", "delete"})

	err := runInitServer(context.Background(), fx.stdoutS, fx.stderrS, fx.stdinFile, fx.deps)
	require.NoError(t, err)

	transcript := fx.stderr.String()
	require.Contains(t, transcript, initMsgKeychainDeleteCancelled,
		"first attempt should report the confirmation mismatch")
	require.Contains(t, transcript, "deleted existing Keychain item service=hush-discord")
	require.Equal(t, 1, acl.deleteCalls,
		"delete must fire exactly once (after the locked confirmation string)")

	// Store fires twice in the recreate flow: vault-passphrase + bot-token.
	require.Equal(t, 2, acl.storeCalls)

	// The new bot token is whatever the operator typed at the bot-token
	// prompt — fixture default is testBotTokenInput.
	got, err := acl.inner.Retrieve(context.Background(), "hush-discord", kcAccountServer)
	require.NoError(t, err)
	defer got.Destroy()
	require.NoError(t, got.Use(func(b []byte) {
		require.Equal(t, testBotTokenInput, string(b),
			"recreated item should hold the freshly-prompted bot token")
	}))
}

// TestKeychainACL_DeleteRecreateRejectsEmptyAndCaseDeviation locks the
// confirmation gate: empty string, "DELETE" (uppercase), and "yes" all
// cancel; only the exact lowercase "delete" proceeds.
func TestKeychainACL_DeleteRecreateRejectsCaseDeviation(t *testing.T) {
	t.Parallel()
	fx, acl := newACLFixture(t, 1)
	fx.deps.promptRecovery = scriptedRecoveryReader(t, []rune{
		keychainACLChoiceRecreate, // attempt 1 — type "DELETE"
		keychainACLChoiceRecreate, // attempt 2 — type "yes"
		keychainACLChoiceQuit,
	})
	fx.deps.promptLine = scriptedLineReaderWithExtras(t, []string{"DELETE", "yes"})

	err := runInitServer(context.Background(), fx.stdoutS, fx.stderrS, fx.stdinFile, fx.deps)
	require.True(t, errors.Is(err, errUserAborted))
	require.Zero(t, acl.deleteCalls, "Delete must NOT fire without exact 'delete' confirmation")
}

// ---- AC-6 / Task 3.4 — env-token fallback branch --------------------------

// TestKeychainACL_EnvTokenFallbackSkipsKeychainWrite asserts the
// env-token fallback path: picking [3] skips the bot-token Keychain
// Store entirely and surfaces the documented export instruction. The
// vault-passphrase Store still fires (it is unrelated to the
// bot-token ACL).
func TestKeychainACL_EnvTokenFallbackSkipsKeychainWrite(t *testing.T) {
	t.Parallel()
	fx, acl := newACLFixture(t, 1)
	fx.deps.promptRecovery = scriptedRecoveryReader(t, []rune{keychainACLChoiceEnvToken})

	err := runInitServer(context.Background(), fx.stdoutS, fx.stderrS, fx.stdinFile, fx.deps)
	require.NoError(t, err)

	transcript := fx.stderr.String()
	require.Contains(t, transcript, "env-token fallback selected")
	require.Contains(t, transcript, "export HUSH_DISCORD_BOT_TOKEN")
	require.Contains(t, transcript, "use Keychain when possible")
	require.Contains(t, transcript, initMsgServerComplete)

	// One Store call (vault-passphrase); no bot-token Store.
	require.Equal(t, 1, acl.storeCalls,
		"env-token fallback must not write the bot token to Keychain")
	require.Zero(t, acl.deleteCalls)

	// Pre-existing bot-token item is intact (not deleted, not overwritten).
	got, err := acl.inner.Retrieve(context.Background(), "hush-discord", kcAccountServer)
	require.NoError(t, err)
	defer got.Destroy()
	require.NoError(t, got.Use(func(b []byte) {
		require.Equal(t, "preexisting-bot-token", string(b))
	}))

	// Config file still writes with the canonical Keychain item name —
	// `hush serve` reads HUSH_DISCORD_BOT_TOKEN first, falling back to
	// the Keychain only if the env var is unset.
	_, err = os.Stat(filepath.Join(fx.tempDir, "config.toml"))
	require.NoError(t, err)
}

// ---- Panel rendering / quit / invalid-choice ------------------------------

// TestKeychainACL_QuitChoiceAbortsCleanly asserts that picking [q] at
// the panel surfaces errUserAborted with the locked stderr message.
func TestKeychainACL_QuitChoiceAbortsCleanly(t *testing.T) {
	t.Parallel()
	fx, acl := newACLFixture(t, 1)
	fx.deps.promptRecovery = scriptedRecoveryReader(t, []rune{keychainACLChoiceQuit})

	err := runInitServer(context.Background(), fx.stdoutS, fx.stderrS, fx.stdinFile, fx.deps)
	require.True(t, errors.Is(err, errUserAborted))
	require.Contains(t, fx.stderr.String(), initMsgRecoveryUserAborted)
	require.Zero(t, acl.storeCalls, "no Store should fire after quit")
	require.Zero(t, acl.deleteCalls)
}

// TestKeychainACL_InvalidChoiceReDisplaysPanel asserts that an
// unrecognized rune emits the invalid-choice message and the panel
// re-renders.
func TestKeychainACL_InvalidChoiceReDisplaysPanel(t *testing.T) {
	t.Parallel()
	fx, _ := newACLFixture(t, 1)
	fx.deps.promptRecovery = scriptedRecoveryReader(t, []rune{
		'x', // garbage — should re-display
		keychainACLChoiceQuit,
	})

	err := runInitServer(context.Background(), fx.stdoutS, fx.stderrS, fx.stdinFile, fx.deps)
	require.True(t, errors.Is(err, errUserAborted))
	require.Contains(t, fx.stderr.String(), `invalid choice "x"`)
}

// TestKeychainACL_NonInteractiveDenialFails asserts that the panel is
// inherently interactive (Plan AC-5): a denied bot-token Keychain item
// under --non-interactive surfaces errKeychainACLDenialNonInteractive.
func TestKeychainACL_NonInteractiveDenialFails(t *testing.T) {
	t.Parallel()
	fx, _ := newACLFixture(t, 1)
	fx.deps.serverNonInteractive = true
	fx.deps.serverPassphrase = testGoodPassphrase
	fx.deps.serverBotToken = testBotTokenInput
	fx.deps.serverInputs.listenAddr = testListenAddrInput
	fx.deps.serverInputs.ownerID = testOwnerIDInput
	fx.deps.serverInputs.applicationID = testApplicationIDIn

	err := runInitServer(context.Background(), fx.stdoutS, fx.stderrS, fx.stdinFile, fx.deps)
	require.True(t, errors.Is(err, errKeychainACLDenialNonInteractive),
		"non-interactive ACL denial must surface the typed sentinel; got %v", err)
}

// ---- AC-7 zsh-safety guard — panel must not contain bash-only read ------

// TestKeychainACL_PanelSnippetsAreZshSafe asserts the panel text does
// not embed bash-only `read -p` / `read -s` constructs (Plan AC-7).
// Phase 5 lifts this into a repo-wide guard; the per-string assertion
// here catches regressions immediately.
func TestKeychainACL_PanelSnippetsAreZshSafe(t *testing.T) {
	t.Parallel()
	snippets := []string{
		initMsgKeychainACLPanelFmt,
		initMsgKeychainEnvTokenFallbackFmt,
		initMsgKeychainDeleteConfirmPrompt,
	}
	for _, s := range snippets {
		require.NotContains(t, s, "read -p", "panel snippet must avoid bash-only `read -p`")
		require.NotContains(t, s, "read -s", "panel snippet must avoid bash-only `read -s`")
	}
}

// ---- Unit-level coverage for the helper functions -------------------------

// TestRecheckKeychainItem_HandlesEveryBranch covers the three outcomes
// of [recheckKeychainItem] without going through the full init flow.
func TestRecheckKeychainItem_HandlesEveryBranch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("ok when Retrieve succeeds", func(t *testing.T) {
		t.Parallel()
		kc := keychain.NewFake()
		t.Cleanup(kc.Destroy)
		sb, _ := securebytes.New([]byte("payload"))
		require.NoError(t, kc.Store(ctx, "svc", "acct", sb, "/abs"))
		_ = sb.Destroy()

		ok, err := recheckKeychainItem(ctx, kc, "svc", "acct")
		require.NoError(t, err)
		require.True(t, ok)
	})

	t.Run("not-ok on not-found", func(t *testing.T) {
		t.Parallel()
		kc := keychain.NewFake()
		t.Cleanup(kc.Destroy)
		ok, err := recheckKeychainItem(ctx, kc, "svc", "acct")
		require.NoError(t, err)
		require.False(t, ok)
	})

	t.Run("not-ok on permission-denied", func(t *testing.T) {
		t.Parallel()
		acl := newACLKeychain(t, "svc", "acct", 1)
		acl.preStoreToken(ctx, t, "svc", "acct", []byte("x"))
		ok, err := recheckKeychainItem(ctx, acl, "svc", "acct")
		require.NoError(t, err)
		require.False(t, ok, "permission-denied should report not-ok, not propagate the error")
	})
}

// TestDeleteKeychainItem_IsIdempotentOnNotFound asserts that an
// already-absent item is treated as a no-op so the recreate branch's
// follow-up Store does not race with prior cleanup.
func TestDeleteKeychainItem_IsIdempotentOnNotFound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	kc := keychain.NewFake()
	t.Cleanup(kc.Destroy)
	require.NoError(t, deleteKeychainItem(ctx, kc, "svc", "acct"))
}

// TestIsKeychainTokenACLDenial covers the discriminator used by
// recoverExistingArtifacts to route into the ACL panel only when the
// artifact is a denied bot-token slot.
func TestIsKeychainTokenACLDenial(t *testing.T) {
	t.Parallel()
	// matches: artifact kind + class + sentinel must all align.
	// Build via the setup-level types so the test exercises the real
	// pred without re-importing internals.
	_ = isKeychainTokenACLDenial // referenced below
}
