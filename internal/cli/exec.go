// Package cli — child-env construction + os/exec wrapper for `hush
// request --exec`. Also hosts the `--format eval` writer + locked
// WARNING string.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/mrz1836/hush/internal/vault/securebytes"
)

// formatEvalWarning is the locked stderr WARNING printed after the
// last export line when --format eval is used. Byte-equal asserted by
// TestRequest_FormatEvalEmitsStderrWarning. Source of truth:
// docs/SECURITY.md §6 + contracts/cli-request.md §3.
const formatEvalWarning = "WARNING: --format eval prints secret values to stdout. " +
	"They may be captured by terminal scrollback, tmux, or script. " +
	"Use --exec whenever possible.\n"

// buildChildEnv produces the child process's environment by starting
// from parentEnv (with any pre-existing scope-named entries stripped)
// and appending NAME=VALUE entries built inside SecureBytes.Use
// callbacks. The returned []string contains plaintext secret bytes;
// it is owned by exec.Cmd.Env until the exec syscall returns. See
// SECURITY.md §6 for the residual-risk note.
//
// A non-nil error from any SecureBytes.Use is propagated rather than
// silently dropping the entry — the operator must see "secret was not
// available" rather than running a child that thinks the variable is
// simply unset.
//
//nolint:gocognit // sequential parent-strip + per-scope materialize; complexity is structural
func buildChildEnv(scope []string, secrets []*securebytes.SecureBytes, parentEnv []string) ([]string, error) {
	skip := make(map[string]struct{}, len(scope))
	for _, name := range scope {
		skip[name] = struct{}{}
	}
	env := make([]string, 0, len(parentEnv)+len(scope))
	for _, kv := range parentEnv {
		eq := strings.IndexByte(kv, '=')
		if eq <= 0 {
			env = append(env, kv)
			continue
		}
		if _, drop := skip[kv[:eq]]; drop {
			continue
		}
		env = append(env, kv)
	}
	for i, name := range scope {
		if i >= len(secrets) || secrets[i] == nil {
			continue
		}
		if err := secrets[i].Use(func(b []byte) {
			env = append(env, name+"="+string(b))
		}); err != nil {
			return nil, fmt.Errorf("hush/cli: request: materialize %s: %w", name, err)
		}
	}
	return env, nil
}

// runChild resolves program via deps.looker, constructs an
// exec.CommandContext with the supplied env, wires stdin/stdout/stderr
// to the parent's, and returns the child's exit code wrapped in
// *errChildExitCode for mapErr propagation.
func preflightExecProgram(deps requestDeps, program string, stderr *Stream) (string, error) {
	resolved, err := deps.looker(program)
	if err != nil {
		_ = stderr.WriteText("hush: request: --exec program %q not found: %s", program, err)
		return "", fmt.Errorf("hush/cli: request: lookup %q: %w", program, err)
	}
	return resolved, nil
}

func runChild(ctx context.Context, deps requestDeps, program string, childArgs, env []string, stderr *Stream) error {
	resolved, err := preflightExecProgram(deps, program, stderr)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, resolved, childArgs...) //nolint:gosec // operator-supplied program path; LookPath-resolved
	cmd.Path = resolved
	cmd.Env = env
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	runErr := deps.runner(cmd)
	if runErr == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		return &errChildExitCode{code: exitErr.ExitCode()}
	}
	return fmt.Errorf("hush/cli: request: run %q: %w", program, runErr)
}

// escapeShellSingleQuote replaces every `'` byte with the four-byte
// sequence `'\”`. The result is safe to embed inside a single-quoted
// shell literal.
func escapeShellSingleQuote(raw []byte) string {
	if !bytesContainsByte(raw, '\'') {
		return string(raw)
	}
	var b strings.Builder
	b.Grow(len(raw) + 8)
	for _, c := range raw {
		if c == '\'' {
			b.WriteString(`'\''`)
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

// bytesContainsByte returns true when raw contains the supplied byte.
// Avoids the bytes package import for one call.
func bytesContainsByte(raw []byte, target byte) bool {
	for _, c := range raw {
		if c == target {
			return true
		}
	}
	return false
}

// renderEvalLine returns one POSIX-shell-evalable export line for the
// given (scope name, secret bytes) pair. Embedded single quotes are
// escaped via the close-quote / backslash-quote / open-quote idiom.
func renderEvalLine(name string, raw []byte) string {
	return "export " + name + "='" + escapeShellSingleQuote(raw) + "'\n"
}

// writeEvalExports renders one export line per scope to stdout (in
// flag-supplied order, NOT server-sorted) and writes the locked
// WARNING to stderr.
func writeEvalExports(stdout, stderr *Stream, scope []string, secrets []*securebytes.SecureBytes) error {
	for i, name := range scope {
		if i >= len(secrets) || secrets[i] == nil {
			continue
		}
		var (
			line   string
			useErr error
		)
		useErr = secrets[i].Use(func(b []byte) {
			line = renderEvalLine(name, b)
		})
		if useErr != nil {
			return useErr
		}
		if _, err := io.WriteString(stdout.w, line); err != nil {
			return err
		}
	}
	if _, err := io.WriteString(stderr.w, formatEvalWarning); err != nil {
		return err
	}
	return nil
}
