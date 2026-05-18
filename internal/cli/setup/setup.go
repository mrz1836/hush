package setup

import (
	"context"
	"fmt"
	"strings"
)

// Status is the verdict a single preflight Check returns.
//
// The set is small on purpose: every diagnostic outcome must fit
// one of these three buckets so the registry can render a
// deterministic report. New states require updating both the
// String method and every existing exhaustive switch.
type Status uint8

const (
	// StatusUnknown is the zero value and indicates a Check did not
	// populate the result. Registry.Run treats it as a programmer
	// error: a Check that returns Status==StatusUnknown is wrapped
	// into a fail with [ErrCheckIncomplete].
	StatusUnknown Status = iota

	// StatusOK signals the environment satisfies the Check.
	StatusOK

	// StatusWarn signals a non-blocking deviation from the
	// recommended posture. The guided flow shows the detail and
	// asks the user to confirm before continuing.
	StatusWarn

	// StatusFail signals a blocking failure. The guided flow exits
	// non-zero with the Check's remedy hint.
	StatusFail
)

// String returns the lowercase token used in human-readable reports
// and in tests that assert the report shape. Order is locked.
func (s Status) String() string {
	switch s {
	case StatusOK:
		return "ok"
	case StatusWarn:
		return "warn"
	case StatusFail:
		return "fail"
	case StatusUnknown:
		fallthrough
	default:
		return "unknown"
	}
}

// SetupCheckResult is the per-check verdict produced by a [Check].
//
// Detail carries a short, human-readable explanation suitable for
// the rendered report (e.g. "config path /Users/x/.hush/config.toml
// is not writable"). It MUST NOT contain a Keychain item value, bot
// token byte, or vault byte (Constitution X).
//
// Err is the underlying typed error, when one applies. Callers
// match it with errors.Is against the [Err*] sentinels defined in
// errors.go to drive remediation branching.
//
// RemedyHint is a one-line, copy-pasteable next step. When Err is a
// sentinel that exposes [RemedyHinter], the registry copies the
// hint into this field so the report is self-contained.
type SetupCheckResult struct {
	Name       string
	Status     Status
	Detail     string
	Err        error
	RemedyHint string
}

// Check is one preflight diagnostic. Implementations are pure
// functions of the environment: no prompts, no audit writes, no
// side effects beyond reading the filesystem / Keychain / network
// adapter state.
//
// Name returns the deterministic identifier used in the report
// and in tests. Names follow snake_case (e.g. "config_target",
// "keychain_readability").
//
// Run is ctx-cancellable. Implementations MUST honor ctx
// cancellation and SHOULD finish in well under a second so the
// guided flow's preflight pass stays sub-3-second on a healthy
// host.
type Check interface {
	Name() string
	Run(ctx context.Context) SetupCheckResult
}

// Report is the ordered slice of per-check results produced by a
// [Registry.Run] pass. It exposes accessors so the guided flow
// does not need to re-walk the slice when rendering or branching.
type Report struct {
	Results []SetupCheckResult
}

// FirstFail returns the first result whose Status is [StatusFail],
// or nil if every check passed or only warned.
func (r Report) FirstFail() *SetupCheckResult {
	for i := range r.Results {
		if r.Results[i].Status == StatusFail {
			return &r.Results[i]
		}
	}
	return nil
}

// Warnings returns every result whose Status is [StatusWarn], in
// the order checks were registered.
func (r Report) Warnings() []SetupCheckResult {
	var out []SetupCheckResult
	for _, res := range r.Results {
		if res.Status == StatusWarn {
			out = append(out, res)
		}
	}
	return out
}

// OK reports whether every check returned [StatusOK]. Warnings are
// not OK — the guided flow must surface them.
func (r Report) OK() bool {
	for _, res := range r.Results {
		if res.Status != StatusOK {
			return false
		}
	}
	return true
}

// String renders a deterministic, multi-line summary suitable for
// debugging or test snapshot comparison. Each line is
// "<name>: <status>[ — <detail>]".
func (r Report) String() string {
	if len(r.Results) == 0 {
		return ""
	}
	var b strings.Builder
	for i, res := range r.Results {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(res.Name)
		b.WriteString(": ")
		b.WriteString(res.Status.String())
		if res.Detail != "" {
			b.WriteString(" — ")
			b.WriteString(res.Detail)
		}
	}
	return b.String()
}

// fail constructs a [StatusFail] result for a Check, attaching the
// supplied error and (when the error implements [RemedyHinter])
// its remedy hint. Internal helper used by both the registry and
// the classifier wrappers.
func fail(name string, err error, detail string) SetupCheckResult {
	res := SetupCheckResult{
		Name:   name,
		Status: StatusFail,
		Detail: detail,
		Err:    err,
	}
	var rh RemedyHinter
	if hinterAs(err, &rh) {
		res.RemedyHint = rh.RemedyHint()
	}
	return res
}

// ok constructs a [StatusOK] result for a Check. Detail is
// optional and may be empty for boring successes.
func ok(name, detail string) SetupCheckResult {
	return SetupCheckResult{Name: name, Status: StatusOK, Detail: detail}
}

// warn constructs a [StatusWarn] result. The detail is required
// because the user is asked to confirm based on what it says.
func warn(name, detail string) SetupCheckResult {
	return SetupCheckResult{Name: name, Status: StatusWarn, Detail: detail}
}

// incomplete wraps a Check that returned StatusUnknown — a
// programmer error caught at registry time. The supervisor never
// reaches the guided flow with an incomplete result; instead the
// registry exits with [ErrCheckIncomplete].
func incomplete(name string) SetupCheckResult {
	err := fmt.Errorf("%w: %s", ErrCheckIncomplete, name)
	return fail(name, err, "check did not populate a status")
}
