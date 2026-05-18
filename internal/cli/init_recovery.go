package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/mrz1836/hush/internal/cli/setup"
)

// Static error sentinels backing the per-call fmt.Errorf wraps below.
// Declaring these as package-scope sentinels satisfies err113 and
// gives tests a stable target for errors.Is checks.
var (
	errEmptyRecoveryChoice   = errors.New("hush/cli: init: empty recovery choice")
	errNoRecoverySeam        = errors.New("hush/cli: init: interactive recovery prompt requested but no TTY seam wired")
	errInvalidRecoveryChoice = errors.New("hush/cli: init: invalid recovery choice")
)

// recoveryDecisions records the per-artifact recovery mode the
// operator selected (or --on-existing fixed) during the guided init
// flow's existing-state recovery pass (Plan AC-9 / Task 2.4). Modes
// are the on-existing enum values; absent artifacts carry no entry.
type recoveryDecisions struct {
	byKind map[setup.ArtifactKind]string
}

// newRecoveryDecisions returns an empty decisions map.
func newRecoveryDecisions() recoveryDecisions {
	return recoveryDecisions{byKind: make(map[setup.ArtifactKind]string)}
}

// modeFor returns the recorded mode for kind, or onExistingFail when
// none is recorded — the safe "no decision" default that drops
// callers back into the legacy refuse-on-existence path.
func (r recoveryDecisions) modeFor(kind setup.ArtifactKind) string {
	if r.byKind == nil {
		return onExistingFail
	}
	m, ok := r.byKind[kind]
	if !ok {
		return onExistingFail
	}
	return m
}

// reuseArtifact reports whether the operator chose to keep (reuse or
// repair) the existing artifact of the supplied kind. Used by
// runInitServer to skip the corresponding create step. Repair is
// treated as silent reuse in Phase 2; Phase 3 wires Keychain repair
// flows and may flip this.
func reuseArtifact(d recoveryDecisions, kind setup.ArtifactKind) bool {
	mode := d.modeFor(kind)
	return mode == onExistingReuse || mode == onExistingRepair
}

// validateOnExisting returns an error when the supplied --on-existing
// value is outside the locked enum. An empty string is valid — it
// signals "use the per-mode default" (prompt in interactive, fail in
// non-interactive).
func validateOnExisting(v string) error {
	switch v {
	case "", onExistingPrompt, onExistingReuse, onExistingRepair, onExistingArchive, onExistingFail:
		return nil
	default:
		return fmt.Errorf("%w: --on-existing=%q", errMissingFlag, v)
	}
}

// handlePreflightReport processes a preflight [setup.Report]: the
// first `fail` short-circuits with a typed error; warnings are
// surfaced. The guided flow's promise (Plan AC-2) is "no prompt
// fires until preflight settles" — the caller honors this by only
// proceeding when this function returns nil.
func handlePreflightReport(report setup.Report, deps *initDeps, stderr *Stream) error {
	if first := report.FirstFail(); first != nil {
		hint := first.RemedyHint
		if hint == "" {
			hint = "no remedy hint registered for this preflight slot"
		}
		_ = stderr.WriteText(initMsgPreflightFailFmt, first.Name, first.Detail, hint)
		if first.Err != nil {
			return fmt.Errorf("%w: %s: %w", errPreflightFailed, first.Name, first.Err)
		}
		return fmt.Errorf("%w: %s", errPreflightFailed, first.Name)
	}
	for _, w := range report.Warnings() {
		// Clock-sync warnings are folded silently when the operator
		// already opted into --allow-clock-skew; otherwise we just
		// surface the detail. Phase 4 wires the y/n confirm flow.
		if w.Name == string(setup.CheckClockSync) && deps.serverAllowClockSkew {
			_ = stderr.WriteText(initMsgClockSkewOverrideFmt, w.Detail)
			continue
		}
		_ = stderr.WriteText(initMsgPreflightWarnFmt, w.Name, w.Detail)
	}
	return nil
}

// recoverExistingArtifacts runs the existing-state classifier against
// the resolved init paths + the Discord bot-token Keychain item, and
// asks the operator (or consumes --on-existing) to choose a recovery
// action for each non-absent artifact. The returned decisions feed
// the create steps in runInitServer (Plan AC-9 / Task 2.4).
//
//nolint:gocognit,gocyclo,cyclop // matrix dispatch over artifact kind × mode is structural
func recoverExistingArtifacts(
	ctx context.Context,
	in *os.File,
	stderr *Stream,
	deps *initDeps,
	vaultPath, configPath, stateDir string,
	keychainItems serverKeychainItemNames,
	explicitStateDir bool,
) (recoveryDecisions, error) {
	decisions := newRecoveryDecisions()

	// Drop the Keychain probe when --state-dir is explicit: that
	// flow skips Keychain writes entirely, so a pre-existing
	// bot-token item is neither read nor written.
	inputs := setup.StateInputs{
		ConfigPath: configPath,
		VaultPath:  vaultPath,
		StateDir:   stateDir,
	}
	if !explicitStateDir {
		inputs.KeychainItem = setup.KeychainTarget{
			Service: keychainItems.discordService,
			Account: kcAccountServer,
		}
	}

	classifier := &setup.Classifier{
		Keychain: deps.keychain,
		Now:      deps.nowFn,
	}
	report := classifier.ClassifyState(ctx, inputs)

	for i := range report.Artifacts {
		a := report.Artifacts[i]
		if a.Class == setup.ClassificationAbsent || a.Class == setup.ClassificationUnknown {
			continue
		}
		// State-dir + SafeToReuse is the universal "I re-ran init in
		// the same parent dir" case; silently reuse so the operator is
		// not prompted on every re-run. Every other artifact + every
		// other class still goes through the prompt / on-existing
		// dispatch so a vault, config, or Keychain item is never
		// reused without an explicit confirmation (Plan AC-9).
		if a.Kind == setup.ArtifactStateDir && a.Class == setup.ClassificationSafeToReuse {
			decisions.byKind[a.Kind] = onExistingReuse
			continue
		}
		// ACL-denied bot-token Keychain item gets the specialised
		// recovery panel (Plan AC-5 / AC-6 / Task 3.2-3.4). The panel
		// returns one of the legal decisions (reuse / recreate /
		// env-token) and handles destructive deletes inline so the
		// caller's Store call lands on a clean slot.
		if isKeychainTokenACLDenial(a) {
			mode, err := resolveKeychainACL(ctx, in, stderr, deps, keychainItems.discordService, kcAccountServer)
			if err != nil {
				return decisions, err
			}
			decisions.byKind[a.Kind] = mode
			continue
		}
		mode, err := resolveRecoveryMode(in, stderr, deps, a)
		if err != nil {
			return decisions, err
		}
		decisions.byKind[a.Kind] = mode
		if mode == onExistingArchive && a.Path != "" {
			dst, archErr := setup.Archive(a.Path, deps.nowFn())
			if archErr != nil {
				return decisions, fmt.Errorf("hush/cli: init: archive %s: %w", a.Path, archErr)
			}
			_ = stderr.WriteText(initMsgRecoveryArchivedFmt, a.Path, dst)
		}
		if mode == onExistingRepair {
			_ = stderr.WriteText(initMsgRecoveryRepairFmt, a.Kind)
		}
	}
	return decisions, nil
}

// resolveRecoveryMode maps an [setup.Artifact] + the operator's
// --on-existing / interactive choice to one of the on-existing enum
// values. `q` surfaces as [errUserAborted].
func resolveRecoveryMode(in *os.File, stderr *Stream, deps *initDeps, a setup.Artifact) (string, error) {
	mode := deps.serverOnExisting
	if mode == "" {
		if deps.serverNonInteractive {
			mode = onExistingFail
		} else {
			mode = onExistingPrompt
		}
	}
	if mode != onExistingPrompt {
		return mode, nil
	}
	if deps.promptRecovery == nil {
		return "", errNoRecoverySeam
	}
	pathOrDash := a.Path
	if pathOrDash == "" {
		pathOrDash = "(keychain item)"
	}
	label := fmt.Sprintf(initMsgRecoveryPromptFmt, a.Kind, a.Class, pathOrDash)
	ch, err := deps.promptRecovery(in, stderr.w, label)
	if err != nil {
		return "", err
	}
	return mapRecoveryChoice(ch, stderr)
}

// mapRecoveryChoice translates a single recovery rune into one of
// the on-existing enum values, or surfaces a typed sentinel. Split
// out so resolveRecoveryMode stays within gocyclo's complexity bar.
func mapRecoveryChoice(ch rune, stderr *Stream) (string, error) {
	switch ch {
	case recoveryChoiceReuse:
		return onExistingReuse, nil
	case recoveryChoiceRepair:
		return onExistingRepair, nil
	case recoveryChoiceArchive:
		return onExistingArchive, nil
	case recoveryChoiceQuit:
		_ = stderr.WriteText(initMsgRecoveryUserAborted)
		return "", errUserAborted
	default:
		return "", fmt.Errorf("%w: %q", errInvalidRecoveryChoice, string(ch))
	}
}

// applyLegacyKeychainGuards preserves the legacy Keychain-existence
// refusal sequence under --on-existing=fail and continues to enforce
// the vault-passphrase guard unconditionally (that item is not part
// of the classifier surface). Extracted so runInitServer stays
// under nestif's nesting bar.
func applyLegacyKeychainGuards(
	ctx context.Context,
	deps *initDeps,
	stderr *Stream,
	decisions recoveryDecisions,
	keychainItems serverKeychainItemNames,
	explicitStateDir bool,
) error {
	if explicitStateDir {
		return nil
	}
	if err := guardKeychainAbsent(ctx, deps.keychain, keychainItems.vaultPassphraseService, kcAccountServer, stderr); err != nil {
		return err
	}
	if decisions.modeFor(setup.ArtifactKeychainToken) != onExistingFail {
		return nil
	}
	return guardKeychainAbsent(ctx, deps.keychain, keychainItems.discordService, kcAccountServer, stderr)
}
