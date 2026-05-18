package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mrz1836/hush/internal/cli/setup"
	"github.com/mrz1836/hush/internal/keychain"
)

// maxKeychainACLPanelIterations bounds the operator's panel re-display
// loop so a misbehaving prompt seam in tests cannot spin forever. Real
// operators only ever loop because the ACL repair did not take —
// 5 attempts is generous.
const maxKeychainACLPanelIterations = 5

// errKeychainACLLoopExhausted fires when the operator has cycled
// through the panel more times than [maxKeychainACLPanelIterations]
// without picking a terminal option.
var errKeychainACLLoopExhausted = errors.New("hush/cli: init: Keychain ACL recovery loop exhausted")

// errKeychainACLDenialNonInteractive fires when the bot-token
// Keychain probe surfaces [setup.ErrTokenDenied] in --non-interactive
// mode. The panel is inherently interactive; non-interactive callers
// must re-run with a TTY to choose a recovery branch (Plan AC-5).
var errKeychainACLDenialNonInteractive = errors.New("hush/cli: init: bot-token Keychain item denied and --non-interactive set; re-run interactively to choose a recovery branch")

// resolveKeychainACL drives the ACL-aware recovery panel for a
// bot-token Keychain artifact classified [setup.ClassificationRepairable]
// with [setup.ErrTokenDenied]. It returns one of the per-artifact
// decision modes:
//
//   - [onExistingReuse]   — operator picked [1] ACL repair and the
//     re-check succeeded; the existing item is reused as-is.
//   - [onExistingRecreate] — operator picked [2] delete-and-recreate
//     and typed the locked confirmation; the existing item has
//     already been deleted by this function so the caller's Store
//     call lands on a clean slot.
//   - [onExistingEnvToken] — operator picked [3] env-token fallback;
//     caller must skip the bot-token Keychain Store.
//
// `q` or any I/O error surfaces as [errUserAborted] / the underlying
// I/O error.
//
//nolint:gocognit,gocyclo,cyclop // panel loop is a structural state machine; flattening hurts readability
func resolveKeychainACL(
	ctx context.Context,
	in *os.File,
	stderr *Stream,
	deps *initDeps,
	service, account string,
) (string, error) {
	if deps.serverNonInteractive {
		return "", errKeychainACLDenialNonInteractive
	}
	if deps.promptRecovery == nil {
		return "", errNoRecoverySeam
	}
	keychainPath := loginKeychainPath()

	for attempt := 0; attempt < maxKeychainACLPanelIterations; attempt++ {
		renderKeychainACLPanel(stderr, service, account, keychainPath)
		ch, err := deps.promptRecovery(in, stderr.w, initMsgKeychainACLChoicePrompt)
		if err != nil {
			return "", err
		}
		switch ch {
		case keychainACLChoiceRepair:
			ok, recheckErr := recheckKeychainItem(ctx, deps.keychain, service, account)
			if recheckErr != nil {
				return "", recheckErr
			}
			if ok {
				_ = stderr.WriteText(initMsgKeychainACLRecheckOK, service)
				return onExistingReuse, nil
			}
			_ = stderr.WriteText(initMsgKeychainACLRecheckFailed)
		case keychainACLChoiceRecreate:
			confirmed, confirmErr := confirmKeychainDelete(in, stderr, deps)
			if confirmErr != nil {
				return "", confirmErr
			}
			if !confirmed {
				_ = stderr.WriteText(initMsgKeychainDeleteCancelled)
				continue
			}
			if delErr := deleteKeychainItem(ctx, deps.keychain, service, account); delErr != nil {
				return "", delErr
			}
			_ = stderr.WriteText(initMsgKeychainDeletedFmt, service, account)
			return onExistingRecreate, nil
		case keychainACLChoiceEnvToken:
			_ = stderr.WriteText(initMsgKeychainEnvTokenFallbackFmt)
			return onExistingEnvToken, nil
		case keychainACLChoiceQuit:
			_ = stderr.WriteText(initMsgRecoveryUserAborted)
			return "", errUserAborted
		default:
			_ = stderr.WriteText(initMsgKeychainACLInvalidChoiceFmt, string(ch))
		}
	}
	return "", errKeychainACLLoopExhausted
}

// renderKeychainACLPanel writes the panel text to stderr. Locked
// content — tests assert on substrings. The panel embeds zsh-safe
// snippets only (AC-7); no `read -p` / `read -s`.
func renderKeychainACLPanel(stderr *Stream, service, account, keychainPath string) {
	_ = stderr.WriteText(initMsgKeychainACLPanelFmt,
		service,
		service, account, keychainPath,
		service, account, keychainPath,
		keychainDeleteConfirmation,
	)
}

// loginKeychainPath returns the absolute path of the user's login
// keychain — the file `security set-generic-password-partition-list`
// targets. Tests substitute via $HOME; production resolves it through
// [os.UserHomeDir].
func loginKeychainPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "~/Library/Keychains/login.keychain-db"
	}
	return filepath.Join(home, "Library", "Keychains", "login.keychain-db")
}

// recheckKeychainItem re-runs the Keychain Retrieve and reports
// whether the read now succeeds. ErrKeychainItemNotFound or
// ErrKeychainPermissionDenied surface as (false, nil); the operator
// can pick another panel option. Other errors propagate.
func recheckKeychainItem(ctx context.Context, kc keychain.Keychain, service, account string) (bool, error) {
	sb, err := kc.Retrieve(ctx, service, account)
	if err == nil {
		_ = sb.Destroy()
		return true, nil
	}
	if errors.Is(err, keychain.ErrKeychainItemNotFound) || errors.Is(err, keychain.ErrKeychainPermissionDenied) {
		return false, nil
	}
	return false, fmt.Errorf("hush/cli: init: Keychain re-check: %w", err)
}

// confirmKeychainDelete prompts the operator to type the locked
// confirmation string. Returns (true, nil) only when the trimmed
// input exact-matches [keychainDeleteConfirmation]. Anything else —
// including `y`, `yes`, or `delete!` — returns (false, nil) so the
// caller can re-render the panel.
func confirmKeychainDelete(in *os.File, stderr *Stream, deps *initDeps) (bool, error) {
	line, err := deps.promptLine(in, stderr.w, initMsgKeychainDeleteConfirmPrompt)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(line) == keychainDeleteConfirmation, nil
}

// deleteKeychainItem removes (service, account) from the Keychain.
// An already-absent item is treated as a no-op so the caller's
// follow-up Store call lands on a clean slot regardless.
func deleteKeychainItem(ctx context.Context, kc keychain.Keychain, service, account string) error {
	if err := kc.Delete(ctx, service, account); err != nil {
		if errors.Is(err, keychain.ErrKeychainItemNotFound) {
			return nil
		}
		return fmt.Errorf("hush/cli: init: Keychain delete: %w", err)
	}
	return nil
}

// isKeychainTokenACLDenial reports whether artifact a is the
// bot-token Keychain slot classified as denied (the panel's
// activation condition). Extracted so [recoverExistingArtifacts]
// can branch with a single readable check.
func isKeychainTokenACLDenial(a setup.Artifact) bool {
	if a.Kind != setup.ArtifactKeychainToken {
		return false
	}
	if a.Class != setup.ClassificationRepairable {
		return false
	}
	return errors.Is(a.Err, setup.ErrTokenDenied)
}
