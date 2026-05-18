package setup

import (
	"context"
	"errors"
	"fmt"
)

// CheckName is the locked slot identifier for a preflight check.
// New slots are added by extending [CheckOrder] and the constant
// block below — both in lock step.
type CheckName string

// The slot names are the canonical, snake_case identifiers
// surfaced in [SetupCheckResult.Name], in the rendered report, and
// in tests that pin the order.
const (
	CheckBinaryVersion       CheckName = "binary_version"
	CheckConfigTarget        CheckName = "config_target"
	CheckStateDir            CheckName = "state_dir"
	CheckFileModes           CheckName = "file_modes"
	CheckKeychainReadability CheckName = "keychain_readability"
	CheckTailscaleBind       CheckName = "tailscale_bind"
	CheckListenPort          CheckName = "listen_port"
	CheckClockSync           CheckName = "clock_sync"
	CheckArtifactCollision   CheckName = "artifact_collision"
)

// CheckOrder is the locked execution order the [Registry] honors.
//
// The sequence mirrors the plan in T-278: cheap, deterministic
// checks run first (binary, config, state dir, file modes) so the
// guided flow exits with the most actionable error possible before
// any user-visible delay. Network-touching or OS-dialog-touching
// checks (Keychain, Tailscale, listen port, clock sync) run in the
// second half. The final slot — artifact collision — runs last so
// classification reflects every artifact the earlier checks may
// have observed.
//
// CheckOrder is the public contract; mutating it would silently
// reorder the guided flow.
//
//nolint:gochecknoglobals // ordered slot list is the public contract
var CheckOrder = []CheckName{
	CheckBinaryVersion,
	CheckConfigTarget,
	CheckStateDir,
	CheckFileModes,
	CheckKeychainReadability,
	CheckTailscaleBind,
	CheckListenPort,
	CheckClockSync,
	CheckArtifactCollision,
}

// ErrUnknownCheck is returned by [Registry.Register] when the
// supplied [Check] reports a name that is not a member of
// [CheckOrder]. Adding a new check therefore requires touching
// both [CheckOrder] and the slot constants — by design.
var ErrUnknownCheck = errors.New("hush/setup: unknown check slot")

// Registry is the deterministic preflight runner. It accepts one
// [Check] per slot in [CheckOrder] and runs them in that fixed
// order on [Run]. Unregistered slots are skipped (not failed) so
// later T-278 phases can wire checks in one at a time.
type Registry struct {
	checks map[CheckName]Check
}

// NewRegistry returns an empty Registry. Wire checks via [Register]
// before calling [Run].
func NewRegistry() *Registry {
	return &Registry{checks: make(map[CheckName]Check)}
}

// Register installs the supplied Check under the slot whose name
// matches Check.Name. Returns [ErrUnknownCheck] when Check.Name is
// not in [CheckOrder].
//
// Re-registering the same slot overwrites the previous entry.
// That is intentional: tests stub a slot per case.
func (r *Registry) Register(c Check) error {
	name := CheckName(c.Name())
	if !knownSlot(name) {
		return fmt.Errorf("%w: %q", ErrUnknownCheck, c.Name())
	}
	r.checks[name] = c
	return nil
}

// MustRegister is the panicking variant for production wiring
// where the slot list is constant and an [ErrUnknownCheck] would
// be a programmer error.
func (r *Registry) MustRegister(c Check) {
	if err := r.Register(c); err != nil {
		panic(err)
	}
}

// Registered reports whether a Check is installed under name.
func (r *Registry) Registered(name CheckName) bool {
	_, ok := r.checks[name]
	return ok
}

// Run executes every registered Check in [CheckOrder] and returns
// the assembled [Report]. Unregistered slots are skipped silently.
// A Check that returns [StatusUnknown] is wrapped with
// [ErrCheckIncomplete] — that is a hush bug, not a user problem.
//
// Run honors ctx cancellation between checks: if ctx is done, the
// remaining checks are skipped and the partial report is returned.
func (r *Registry) Run(ctx context.Context) Report {
	out := make([]SetupCheckResult, 0, len(CheckOrder))
	for _, name := range CheckOrder {
		if err := ctx.Err(); err != nil {
			break
		}
		c, ok := r.checks[name]
		if !ok {
			continue
		}
		res := c.Run(ctx)
		if res.Name == "" {
			res.Name = string(name)
		}
		if res.Status == StatusUnknown {
			res = incomplete(string(name))
		}
		out = append(out, res)
	}
	return Report{Results: out}
}

// knownSlot reports whether name is one of the locked slots in
// [CheckOrder].
func knownSlot(name CheckName) bool {
	for _, n := range CheckOrder {
		if n == name {
			return true
		}
	}
	return false
}

// CheckFunc adapts a plain function into a [Check] so callers do
// not need to declare a struct type for each one-off check. The
// returned Check reports `name` from Name() and delegates Run to
// the supplied function.
type CheckFunc struct {
	NameValue string
	RunFn     func(ctx context.Context) SetupCheckResult
}

// Name returns the slot name supplied at construction.
func (c CheckFunc) Name() string { return c.NameValue }

// Run delegates to the wrapped function.
func (c CheckFunc) Run(ctx context.Context) SetupCheckResult {
	if c.RunFn == nil {
		return incomplete(c.NameValue)
	}
	return c.RunFn(ctx)
}

// Ok constructs a [StatusOK] result. Exposed so [Check]
// implementations and tests can build results without re-deriving
// the result struct.
func Ok(name, detail string) SetupCheckResult { return ok(name, detail) }

// Warn constructs a [StatusWarn] result.
func Warn(name, detail string) SetupCheckResult { return warn(name, detail) }

// Fail constructs a [StatusFail] result, copying the err's remedy
// hint into the result when err implements [RemedyHinter].
func Fail(name string, err error, detail string) SetupCheckResult {
	return fail(name, err, detail)
}
