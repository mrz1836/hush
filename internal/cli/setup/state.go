package setup

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/mrz1836/hush/internal/keychain"
)

// Classification is the label the [Classifier] attaches to each
// pre-existing artifact. The guided flow uses these labels to
// branch into reuse / repair / archive prompts per artifact
// (Plan Q2=b).
type Classification uint8

const (
	// ClassificationUnknown is the zero value. The classifier
	// never returns it for a real inspection — it exists so the
	// type's zero state is detectable.
	ClassificationUnknown Classification = iota

	// ClassificationAbsent means no artifact was found at the
	// inspected location. The guided flow can create freshly.
	ClassificationAbsent

	// ClassificationSafeToReuse means the artifact exists and
	// looks structurally valid for the new bootstrap. The guided
	// flow's default action is reuse.
	ClassificationSafeToReuse

	// ClassificationRepairable means the artifact exists but has
	// a fixable issue: loose file mode, partial state (config
	// without vault, vault without config), or an ACL-denied
	// Keychain item that can be repaired in place.
	ClassificationRepairable

	// ClassificationCollision means the artifact exists and
	// conflicts with the new bootstrap in a way that cannot be
	// repaired without losing data (e.g. an explicit --state-dir
	// path is occupied by an unrelated hush install). The guided
	// flow requires explicit archive confirmation before
	// proceeding.
	ClassificationCollision
)

// String returns the lowercase, hyphenated token used in the
// rendered report and in tests pinned on the report shape.
func (c Classification) String() string {
	switch c {
	case ClassificationAbsent:
		return "absent"
	case ClassificationSafeToReuse:
		return "safe-to-reuse"
	case ClassificationRepairable:
		return "repairable"
	case ClassificationCollision:
		return "collision"
	case ClassificationUnknown:
		fallthrough
	default:
		return "unknown"
	}
}

// ArtifactKind identifies which existing-state slot an [Artifact]
// describes. The kinds are deliberately coarse: each maps to one
// place hush keeps state.
type ArtifactKind string

const (
	// ArtifactConfig is the hush TOML config file.
	ArtifactConfig ArtifactKind = "config"
	// ArtifactVault is the encrypted vault file referenced by config.
	ArtifactVault ArtifactKind = "vault"
	// ArtifactStateDir is the directory whose 0700 mode protects
	// state files (sockets, dropped tokens, etc.).
	ArtifactStateDir ArtifactKind = "state_dir"
	// ArtifactKeychainToken is the Darwin Keychain item that holds
	// the Discord bot token.
	ArtifactKeychainToken ArtifactKind = "keychain_token"
)

// Artifact is one classified pre-existing slot.
type Artifact struct {
	Kind   ArtifactKind
	Path   string // empty for ArtifactKeychainToken
	Class  Classification
	Detail string
	Err    error // typed error when Class is Repairable or Collision
}

// StateInputs is the set of paths and identifiers the [Classifier]
// inspects. Fields left empty are skipped (the corresponding
// artifact is omitted from the report).
type StateInputs struct {
	ConfigPath   string
	VaultPath    string
	StateDir     string
	KeychainItem KeychainTarget
}

// KeychainTarget is the (service, account) tuple the classifier
// reads to determine the bot-token artifact's class. A zero-value
// target is treated as "no Keychain probe requested".
type KeychainTarget struct {
	Service string
	Account string
}

// IsZero reports whether the target has neither a service nor an
// account configured.
func (k KeychainTarget) IsZero() bool {
	return k.Service == "" && k.Account == ""
}

// Classifier inspects pre-existing artifacts and produces a
// per-slot [Classification]. Tests substitute the seams (now, fs,
// keychain probe) to drive deterministic scenarios.
type Classifier struct {
	// Now returns the moment used to stamp [Archive]'s suffix.
	// Defaults to time.Now when unset.
	Now func() time.Time
	// Keychain is the Keychain implementation used to probe the
	// bot-token artifact. When nil the keychain artifact is
	// always classified as Absent.
	Keychain keychain.Keychain
	// StatFn overrides os.Stat for tests that need to inject
	// permission errors or alternative file modes.
	StatFn func(path string) (fs.FileInfo, error)
}

// ClassifyState inspects every populated field of inputs and
// returns the per-artifact classification slice in a deterministic
// order: config → vault → state_dir → keychain_token. Artifacts
// whose input is empty are omitted.
func (c *Classifier) ClassifyState(ctx context.Context, inputs StateInputs) StateReport {
	results := make([]Artifact, 0, 4)
	if inputs.ConfigPath != "" {
		results = append(results, c.classifyFile(ArtifactConfig, inputs.ConfigPath, configFileExpectations()))
	}
	if inputs.VaultPath != "" {
		results = append(results, c.classifyFile(ArtifactVault, inputs.VaultPath, vaultFileExpectations()))
	}
	if inputs.StateDir != "" {
		results = append(results, c.classifyDir(ArtifactStateDir, inputs.StateDir))
	}
	if !inputs.KeychainItem.IsZero() {
		results = append(results, c.classifyKeychain(ctx, inputs.KeychainItem))
	}

	c.applyCrossArtifactRules(results)
	return StateReport{Artifacts: results}
}

// Classify returns the same data as [ClassifyState] but reshaped
// into the diagnostic [Report] type so the guided flow can render
// classifier results next to preflight check results in a single
// stream. Note: [Classification] values [ClassificationAbsent]
// and [ClassificationSafeToReuse] both map to [StatusOK]; callers
// that need the finer-grained distinction must use
// [ClassifyState].
func (c *Classifier) Classify(ctx context.Context, inputs StateInputs) Report {
	state := c.ClassifyState(ctx, inputs)
	results := make([]SetupCheckResult, 0, len(state.Artifacts))
	for _, a := range state.Artifacts {
		res := SetupCheckResult{
			Name:   string(a.Kind),
			Status: classToStatus(a.Class),
			Detail: a.Detail,
			Err:    a.Err,
		}
		if a.Err != nil {
			var rh RemedyHinter
			if errors.As(a.Err, &rh) {
				res.RemedyHint = rh.RemedyHint()
			}
		}
		results = append(results, res)
	}
	return Report{Results: results}
}

// fileExpectations is the per-file invariants the classifier
// enforces. modeMask is ANDed against the stat-reported mode; any
// bit set indicates a loose mode. minSize is the smallest sane
// size for a valid artifact.
type fileExpectations struct {
	modeMask fs.FileMode
	minSize  int64
}

func configFileExpectations() fileExpectations {
	// 0600 expected; anything group/other-readable is loose.
	return fileExpectations{modeMask: 0o077, minSize: 1}
}

func vaultFileExpectations() fileExpectations {
	// 0600 expected; loose modes are repairable (we can chmod).
	// minSize is generous — a zero-byte vault is corrupt, but a
	// few bytes is still suspect; the actual decode happens
	// elsewhere.
	return fileExpectations{modeMask: 0o077, minSize: 1}
}

func (c *Classifier) statFn() func(string) (fs.FileInfo, error) {
	if c.StatFn != nil {
		return c.StatFn
	}
	return os.Stat
}

// classifyFile inspects a regular-file artifact. The caller maps
// the absent / safe / repairable / collision verdict back into an
// [Artifact] record.
func (c *Classifier) classifyFile(kind ArtifactKind, path string, exp fileExpectations) Artifact {
	a := Artifact{Kind: kind, Path: path}
	info, err := c.statFn()(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			a.Class = ClassificationAbsent
			return a
		}
		if errors.Is(err, fs.ErrPermission) {
			a.Class = ClassificationRepairable
			a.Detail = fmt.Sprintf("stat denied: %v", err)
			a.Err = err
			return a
		}
		a.Class = ClassificationCollision
		a.Detail = fmt.Sprintf("stat failed: %v", err)
		a.Err = errors.Join(ErrArtifactCollision, err)
		return a
	}
	if info.IsDir() {
		a.Class = ClassificationCollision
		a.Detail = "expected file but found directory"
		a.Err = ErrArtifactCollision
		return a
	}
	if info.Size() < exp.minSize {
		a.Class = ClassificationRepairable
		a.Detail = fmt.Sprintf("file is %d byte(s); expected at least %d", info.Size(), exp.minSize)
		a.Err = ErrStateStale
		return a
	}
	if info.Mode().Perm()&exp.modeMask != 0 {
		a.Class = ClassificationRepairable
		a.Detail = fmt.Sprintf("mode %#o is laxer than 0600", info.Mode().Perm())
		a.Err = ErrStateStale
		return a
	}
	a.Class = ClassificationSafeToReuse
	a.Detail = fmt.Sprintf("mode %#o, %d bytes", info.Mode().Perm(), info.Size())
	return a
}

// classifyDir inspects the state directory. The directory must be
// 0700; anything looser is repairable.
func (c *Classifier) classifyDir(kind ArtifactKind, path string) Artifact {
	a := Artifact{Kind: kind, Path: path}
	info, err := c.statFn()(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			a.Class = ClassificationAbsent
			return a
		}
		if errors.Is(err, fs.ErrPermission) {
			a.Class = ClassificationRepairable
			a.Detail = fmt.Sprintf("stat denied: %v", err)
			a.Err = err
			return a
		}
		a.Class = ClassificationCollision
		a.Detail = fmt.Sprintf("stat failed: %v", err)
		a.Err = errors.Join(ErrArtifactCollision, err)
		return a
	}
	if !info.IsDir() {
		a.Class = ClassificationCollision
		a.Detail = "expected directory but found regular file"
		a.Err = ErrArtifactCollision
		return a
	}
	// Anything group/other-accessible (0077 mask) is loose.
	if info.Mode().Perm()&0o077 != 0 {
		a.Class = ClassificationRepairable
		a.Detail = fmt.Sprintf("mode %#o is laxer than 0700", info.Mode().Perm())
		a.Err = ErrStateStale
		return a
	}
	a.Class = ClassificationSafeToReuse
	a.Detail = fmt.Sprintf("mode %#o", info.Mode().Perm())
	return a
}

// classifyKeychain probes the bot-token Keychain item via the
// configured [keychain.Keychain]. The probed value is destroyed
// immediately — only the verdict travels back to the caller.
func (c *Classifier) classifyKeychain(ctx context.Context, target KeychainTarget) Artifact {
	a := Artifact{Kind: ArtifactKeychainToken}
	if c.Keychain == nil {
		a.Class = ClassificationAbsent
		a.Detail = "no Keychain probe configured"
		return a
	}
	sb, err := c.Keychain.Retrieve(ctx, target.Service, target.Account)
	switch {
	case err == nil:
		// Destroy immediately — we only care about the verdict.
		_ = sb.Destroy()
		a.Class = ClassificationSafeToReuse
		a.Detail = fmt.Sprintf("service=%s account=%s readable", target.Service, target.Account)
	case errors.Is(err, keychain.ErrKeychainItemNotFound):
		a.Class = ClassificationAbsent
		a.Detail = fmt.Sprintf("service=%s account=%s not present", target.Service, target.Account)
	case errors.Is(err, keychain.ErrKeychainPermissionDenied):
		a.Class = ClassificationRepairable
		a.Detail = fmt.Sprintf("service=%s account=%s denied by OS", target.Service, target.Account)
		a.Err = ErrTokenDenied
	default:
		a.Class = ClassificationCollision
		a.Detail = fmt.Sprintf("service=%s account=%s probe failed: %v", target.Service, target.Account, err)
		a.Err = errors.Join(ErrArtifactCollision, err)
	}
	return a
}

// applyCrossArtifactRules promotes per-artifact classifications
// based on companion artifacts. Currently: when one of config /
// vault is safe-to-reuse but the other is absent, the present one
// is marked repairable (partial state).
func (c *Classifier) applyCrossArtifactRules(results []Artifact) {
	configIdx, vaultIdx := -1, -1
	for i := range results {
		if results[i].Kind == ArtifactConfig {
			configIdx = i
		}
		if results[i].Kind == ArtifactVault {
			vaultIdx = i
		}
	}
	if configIdx == -1 || vaultIdx == -1 {
		return
	}
	promoteIfPartial(&results[configIdx], results[vaultIdx], "config present but vault missing — partial state")
	promoteIfPartial(&results[vaultIdx], results[configIdx], "vault present but config missing — partial state")
}

// promoteIfPartial flips present from safe-to-reuse to repairable
// when companion is absent. Extracted so the cross-artifact rules
// stay below gocyclo's complexity ceiling.
func promoteIfPartial(present *Artifact, companion Artifact, detail string) {
	if present.Class == ClassificationSafeToReuse && companion.Class == ClassificationAbsent {
		present.Class = ClassificationRepairable
		present.Detail = detail
		present.Err = ErrStateStale
	}
}

// StateReport is the ordered slice of [Artifact] records returned
// by [Classifier.Classify]. It exposes helpers that mirror
// [Report] without conflating the two types.
type StateReport struct {
	Artifacts []Artifact
}

// FirstCollision returns the first artifact classified as
// [ClassificationCollision], or nil.
func (r StateReport) FirstCollision() *Artifact {
	for i := range r.Artifacts {
		if r.Artifacts[i].Class == ClassificationCollision {
			return &r.Artifacts[i]
		}
	}
	return nil
}

// Repairable returns every artifact classified as
// [ClassificationRepairable] in registration order.
func (r StateReport) Repairable() []Artifact {
	var out []Artifact
	for _, a := range r.Artifacts {
		if a.Class == ClassificationRepairable {
			out = append(out, a)
		}
	}
	return out
}

// AllAbsent reports whether every artifact is
// [ClassificationAbsent] — i.e. a fresh setup with nothing to
// reuse, repair, or archive.
func (r StateReport) AllAbsent() bool {
	for _, a := range r.Artifacts {
		if a.Class != ClassificationAbsent {
			return false
		}
	}
	return true
}

// classToStatus maps a [Classification] to the [Status] used in
// the diagnostic [Report] shape. Note: absent and safe-to-reuse
// both map to [StatusOK] — callers needing the finer distinction
// must use [Classifier.ClassifyState] directly.
func classToStatus(c Classification) Status {
	switch c {
	case ClassificationAbsent, ClassificationSafeToReuse:
		return StatusOK
	case ClassificationRepairable:
		return StatusWarn
	case ClassificationCollision:
		return StatusFail
	case ClassificationUnknown:
		fallthrough
	default:
		return StatusUnknown
	}
}

// Archive moves the file or directory at path to
// <path>.bak-<RFC3339> using os.Rename, which is atomic on POSIX
// for renames within the same filesystem. Returns the new path on
// success.
//
// now is the timestamp source — pass time.Now in production or a
// pinned value in tests. The RFC3339 representation is formatted
// in UTC so test snapshots are timezone-independent and the
// archive name sorts lexicographically by age.
//
// Archive does NOT touch the source if it does not exist; it
// returns the wrapped fs.ErrNotExist instead. This keeps the
// guided flow's archive prompt idempotent across re-runs.
func Archive(path string, now time.Time) (string, error) {
	if path == "" {
		return "", fmt.Errorf("%w: empty path", ErrArtifactCollision)
	}
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("hush/setup: archive stat %s: %w", path, err)
	}
	stamp := now.UTC().Format(time.RFC3339)
	dst := archiveDest(path, stamp)
	if err := os.Rename(path, dst); err != nil {
		return "", fmt.Errorf("hush/setup: archive rename %s -> %s: %w", path, dst, err)
	}
	return dst, nil
}

// archiveDest derives the .bak-<RFC3339> sibling path for path
// with a given stamp. Exposed (lowercase) only so tests can pin
// the naming convention without re-running os.Rename.
func archiveDest(path, stamp string) string {
	// filepath.Clean keeps the suffix attached to the basename
	// and not to a trailing separator on directory paths.
	cleaned := filepath.Clean(path)
	return cleaned + ".bak-" + stamp
}
