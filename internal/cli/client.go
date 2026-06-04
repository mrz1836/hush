package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/mrz1836/hush/internal/supervise"
	"github.com/mrz1836/hush/pkg/client"
)

// statusDoc mirrors the supervisor wire DTO. Used for human-rendering
// on the TTY path. The pipe / `--json` path writes the raw socket bytes
// verbatim so any future supervisor-side field additions pass through
// untouched.
type statusDoc struct {
	Supervisor        string   `json:"supervisor"`
	State             string   `json:"state"`
	SessionExpiresAt  string   `json:"session_expires_at"`
	RefreshWindowNext string   `json:"refresh_window_next"`
	ResealNext        *string  `json:"reseal_next"`
	ScopeHealthy      []string `json:"scope_healthy"`
	ScopeStale        []string `json:"scope_stale"`
	LastAuthFailure   *string  `json:"last_auth_failure"`
	ChildPID          *int     `json:"child_pid"`
	ChildUptime       string   `json:"child_uptime"`
	DiscordConnected  bool     `json:"discord_connected"`
}

// clientStatusTimeout is the wall-clock ceiling on `client
// status`. Overridable from test code via the test-only seam below.
//
//nolint:gochecknoglobals // sentinel-class timeout knob; mutated only by tests via withTimeouts.
var clientStatusTimeout = 2 * time.Second

// clientRefreshTimeout is the wall-clock ceiling on `client
// refresh`. Overridable from test code via the test-only seam below.
//
//nolint:gochecknoglobals // sentinel-class timeout knob; mutated only by tests via withTimeouts.
var clientRefreshTimeout = 90 * time.Second

// clientRenewTimeout is the wall-clock ceiling on `client renew`.
// Approval is human-gated, so the default is intentionally longer than
// the same-host refill/status calls. Overridable from test code.
//
//nolint:gochecknoglobals // sentinel-class timeout knob; mutated only by tests via withTimeouts.
var clientRenewTimeout = 120 * time.Second

// isTerminalFn is the TTY-detect seam. Production wires
// term.IsTerminal; tests override to force one branch or the other.
//
//nolint:gochecknoglobals // sentinel-class test seam; mutated only by tests via withTerminalFn.
var isTerminalFn = func(fd uintptr) bool { return term.IsTerminal(int(fd)) }

// newClientCmd constructs the `hush client` parent. Has no own RunE —
// it namespaces `status` and `refresh`.
func newClientCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "client",
		Short: "Query a running supervisor over its status socket",
	}
	cmd.AddCommand(newClientStatusCmd())
	cmd.AddCommand(newClientRefreshCmd())
	cmd.AddCommand(newClientRenewCmd())
	return cmd
}

// clientStatusFlags holds the parsed flag values for `hush client
// status`.
type clientStatusFlags struct {
	socketPath     string
	supervisorName string
	jsonOutput     bool
}

// newClientStatusCmd constructs the `hush client status` leaf.
func newClientStatusCmd() *cobra.Command {
	flags := clientStatusFlags{}
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Query a running supervisor's status",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runClientStatus(cmd, flags)
		},
	}
	cmd.Flags().StringVar(&flags.socketPath, "socket", "",
		"Absolute path to the supervisor's status socket (wins over --supervisor)")
	cmd.Flags().StringVar(&flags.supervisorName, "supervisor", "",
		"Supervisor name (derives the socket path)")
	cmd.Flags().BoolVar(&flags.jsonOutput, "json", false,
		"Force JSON output regardless of stdout TTY-ness")
	return cmd
}

// clientRefreshFlags holds the parsed flag values for `hush client
// refresh`.
type clientRefreshFlags struct {
	socketPath     string
	supervisorName string
}

// newClientRefreshCmd constructs the `hush client refresh` leaf. No
// --json flag — refresh has no format option.
func newClientRefreshCmd() *cobra.Command {
	flags := clientRefreshFlags{}
	cmd := &cobra.Command{
		Use:   "refresh",
		Short: "Refill secrets under the existing supervisor session",
		Long: strings.TrimSpace(`
Refill supervisor secrets under the existing approved session.

This is a silent secret refill and child restart; it does not request a
fresh Discord approval. Use "hush client renew" when you need a new
operator approval for the next session window.
`),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runClientRefresh(cmd, flags)
		},
	}
	cmd.Flags().StringVar(&flags.socketPath, "socket", "",
		"Absolute path to the supervisor's status socket (wins over --supervisor)")
	cmd.Flags().StringVar(&flags.supervisorName, "supervisor", "",
		"Supervisor name (derives the socket path)")
	return cmd
}

// clientRenewFlags holds the parsed flag values for `hush client
// renew`.
type clientRenewFlags struct {
	socketPath     string
	supervisorName string
	restart        bool
}

// newClientRenewCmd constructs the `hush client renew` leaf. No --json
// flag — renew is an operator workflow with fixed human output.
func newClientRenewCmd() *cobra.Command {
	flags := clientRenewFlags{}
	cmd := &cobra.Command{
		Use:   "renew",
		Short: "Request a fresh supervisor approval",
		Long: strings.TrimSpace(`
Request a fresh supervisor approval through the existing claim flow.

By default renew swaps the approved session without restarting the
supervised child. Pass --restart to restart the child after approval.
`),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runClientRenew(cmd, flags)
		},
	}
	cmd.Flags().StringVar(&flags.socketPath, "socket", "",
		"Absolute path to the supervisor's status socket (wins over --supervisor)")
	cmd.Flags().StringVar(&flags.supervisorName, "supervisor", "",
		"Supervisor name (derives the socket path)")
	cmd.Flags().BoolVar(&flags.restart, "restart", false,
		"Restart the supervised child after approval succeeds")
	return cmd
}

// resolveSocketPath applies the precedence rule:
//  1. --socket <abs-path> wins.
//  2. else --supervisor NAME → supervise.SocketPathForSupervisor.
//  3. else auto-detect via supervise.EnumerateSupervisorSockets:
//     exactly 1 → use it; 0 or >1 → errSocketAmbiguous.
func resolveSocketPath(socket, supervisor string) (string, error) {
	if socket != "" {
		if !filepath.IsAbs(socket) {
			return "", fmt.Errorf("%w: --socket must be an absolute path, got %q", errSocketAmbiguous, socket)
		}
		return socket, nil
	}
	if supervisor != "" {
		if !supervisorSlugRe.MatchString(supervisor) {
			return "", fmt.Errorf("%w: --supervisor must match ^[a-zA-Z0-9_-]+$, got %q", errSocketAmbiguous, supervisor)
		}
		return supervise.SocketPathForSupervisor(supervisor), nil
	}
	candidates, err := supervise.EnumerateSupervisorSockets()
	if err != nil {
		return "", fmt.Errorf("%w: enumerate: %w", errSocketAmbiguous, err)
	}
	switch len(candidates) {
	case 1:
		return candidates[0], nil
	case 0:
		return "", fmt.Errorf("%w: no supervisor sockets found", errSocketAmbiguous)
	default:
		return "", fmt.Errorf("%w: multiple supervisor sockets found: %s", errSocketAmbiguous, strings.Join(candidates, ", "))
	}
}

// supervisorSlugRe mirrors the supervise-package validation pattern.
// Kept duplicated here so the CLI layer can validate user input
// without invoking a panicking helper.
var supervisorSlugRe = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// runClientStatus dials the supervisor socket via pkg/client, then
// dispatches to the human-text or raw-JSON output path per the TTY /
// --json decision (cli-client.md §2.4 / §2.5).
func runClientStatus(cmd *cobra.Command, flags clientStatusFlags) error {
	stderr := cmd.ErrOrStderr()

	path, err := resolveSocketPath(flags.socketPath, flags.supervisorName)
	if err != nil {
		printClientErr(stderr, "status", err)
		return err
	}

	ctx, cancel := context.WithTimeout(cmd.Context(), clientStatusTimeout)
	defer cancel()

	sup := client.NewSupervisorStatus(path)
	body, sdkErr := sup.SnapshotRaw(ctx)
	if sdkErr != nil {
		wrapped := wrapSDKErrAsUnreachable(sdkErr)
		printClientErr(stderr, "status", wrapped)
		return wrapped
	}

	stdout := cmd.OutOrStdout()
	useJSON := flags.jsonOutput
	if !useJSON {
		if f, ok := stdoutFile(stdout); ok {
			useJSON = !isTerminalFn(f.Fd())
		} else {
			useJSON = true
		}
	}
	if useJSON {
		if _, werr := stdout.Write(body); werr != nil {
			return fmt.Errorf("hush: client status: write: %w", werr)
		}
		return nil
	}

	var doc statusDoc
	if jerr := json.Unmarshal(bytes.TrimSpace(body), &doc); jerr != nil {
		wrapped := fmt.Errorf("%w: parse response: %w", errSocketUnreachable, jerr)
		printClientErr(stderr, "status", wrapped)
		return wrapped
	}
	return writeHumanStatus(stdout, doc)
}

func runClientRefresh(cmd *cobra.Command, flags clientRefreshFlags) error {
	stderr := cmd.ErrOrStderr()

	path, err := resolveSocketPath(flags.socketPath, flags.supervisorName)
	if err != nil {
		printClientErr(stderr, "refresh", err)
		return err
	}

	ctx, cancel := context.WithTimeout(cmd.Context(), clientRefreshTimeout)
	defer cancel()

	sup := client.NewSupervisorStatus(path)
	if sdkErr := sup.Refresh(ctx); sdkErr != nil {
		wrapped := wrapRefreshSDKErr(sdkErr)
		printClientErr(stderr, "refresh", wrapped)
		return wrapped
	}
	stdout := cmd.OutOrStdout()
	if _, werr := fmt.Fprintln(stdout, "hush: client refresh: secret refill complete (no re-approval)"); werr != nil {
		return fmt.Errorf("hush: client refresh: write: %w", werr)
	}
	renewTarget := "<name>"
	if flags.supervisorName != "" {
		renewTarget = flags.supervisorName
	}
	if _, werr := fmt.Fprintf(stdout,
		"note: refresh refills secrets under the existing session; to request a fresh Discord approval run: hush client renew --supervisor %s\n",
		renewTarget,
	); werr != nil {
		return fmt.Errorf("hush: client refresh: write: %w", werr)
	}
	return nil
}

func runClientRenew(cmd *cobra.Command, flags clientRenewFlags) error {
	stderr := cmd.ErrOrStderr()

	path, err := resolveSocketPath(flags.socketPath, flags.supervisorName)
	if err != nil {
		printClientErr(stderr, "renew", err)
		return err
	}

	ctx, cancel := context.WithTimeout(cmd.Context(), clientRenewTimeout)
	defer cancel()

	sup := client.NewSupervisorStatus(path)
	res, sdkErr := sup.Renew(ctx, client.RenewOptions{Restart: flags.restart})
	if sdkErr != nil {
		wrapped := wrapRenewSDKErr(sdkErr)
		printClientErr(stderr, "renew", wrapped)
		return wrapped
	}
	stdout := cmd.OutOrStdout()
	outcome := res.Outcome
	if outcome == "" {
		outcome = supervise.RenewOutcomeRenewed
	}
	if outcome != supervise.RenewOutcomeRenewed {
		wrapped := fmt.Errorf("%w renew: unexpected successful outcome %q", errSupervisorRefused, outcome)
		printClientErr(stderr, "renew", wrapped)
		return wrapped
	}
	restartSuffix := ""
	if res.Restarted {
		restartSuffix = "; child restarted"
	}
	if _, werr := fmt.Fprintf(stdout,
		"hush: client renew: session renewed (approval granted); next expiry %s%s\n",
		formatRenewExpiry(res.SessionExpiresAt),
		restartSuffix,
	); werr != nil {
		return fmt.Errorf("hush: client renew: write: %w", werr)
	}
	return nil
}

// wrapSDKErrAsUnreachable translates pkg/client typed errors into the
// CLI's exit-code sentinels. Both ErrSocketUnavailable and
// ErrInvalidResponse map to errSocketUnreachable so the existing
// ExitErr classification is preserved.
func wrapSDKErrAsUnreachable(err error) error {
	switch {
	case errors.Is(err, client.ErrSocketUnavailable),
		errors.Is(err, client.ErrInvalidResponse):
		return fmt.Errorf("%w: %w", errSocketUnreachable, err)
	default:
		return fmt.Errorf("%w: %w", errSocketUnreachable, err)
	}
}

// wrapRefreshSDKErr translates pkg/client refresh errors into CLI
// sentinels. ErrRefreshDenied → errSupervisorRefused (ack returned
// ok=false). Everything else → errSocketUnreachable.
func wrapRefreshSDKErr(err error) error {
	if errors.Is(err, client.ErrRefreshDenied) {
		// Strip the SDK's "hush/client: supervisor refused refresh: "
		// prefix so the surfaced message preserves the existing
		// "<supervisor refused>: <reason>" shape.
		reason := strings.TrimPrefix(err.Error(), client.ErrRefreshDenied.Error()+": ")
		return fmt.Errorf("%w: %s", errSupervisorRefused, reason)
	}
	return fmt.Errorf("%w: %w", errSocketUnreachable, err)
}

// wrapRenewSDKErr translates pkg/client renew errors into CLI
// sentinels. Operator-visible renew refusals keep the existing
// errSupervisorRefused exit class; socket/parse failures remain
// errSocketUnreachable.
func wrapRenewSDKErr(err error) error {
	switch {
	case errors.Is(err, client.ErrRenewDenied):
		return fmt.Errorf("%w renew: %s", errSupervisorRefused, trimSDKReason(err, client.ErrRenewDenied))
	case errors.Is(err, client.ErrRenewTimeout):
		return fmt.Errorf("%w renew: approval timed out", errSupervisorRefused)
	case errors.Is(err, client.ErrRenewRefusedState):
		return fmt.Errorf("%w renew: %s", errSupervisorRefused, trimSDKReason(err, client.ErrRenewRefusedState))
	case errors.Is(err, client.ErrRenewFailed):
		return fmt.Errorf("%w renew: %s", errSupervisorRefused, trimSDKReason(err, client.ErrRenewFailed))
	case errors.Is(err, client.ErrSocketUnavailable),
		errors.Is(err, client.ErrInvalidResponse):
		return fmt.Errorf("%w: %w", errSocketUnreachable, err)
	default:
		return fmt.Errorf("%w: %w", errSocketUnreachable, err)
	}
}

func trimSDKReason(err error, sentinel error) string {
	reason := strings.TrimPrefix(err.Error(), sentinel.Error()+": ")
	if strings.TrimSpace(reason) == "" || reason == err.Error() {
		return err.Error()
	}
	return reason
}

func formatRenewExpiry(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	return t.Format(time.RFC3339)
}

// writeHumanStatus renders doc as the locked human-summary format
// from cli-client.md §2.5.
func writeHumanStatus(w io.Writer, doc statusDoc) error {
	pid := "no child"
	if doc.ChildPID != nil {
		pid = fmt.Sprintf("%d", *doc.ChildPID)
	}
	authFail := "never"
	if doc.LastAuthFailure != nil {
		authFail = *doc.LastAuthFailure
	}
	healthy := joinScopes(doc.ScopeHealthy)
	stale := joinScopes(doc.ScopeStale)
	discord := "disconnected"
	if doc.DiscordConnected {
		discord = "connected"
	}
	resealNext := "none"
	if doc.ResealNext != nil {
		resealNext = *doc.ResealNext
	}
	state := doc.State
	if state == "" {
		state = "(unknown)"
	}
	lines := []string{
		fmt.Sprintf("Supervisor: %s", doc.Supervisor),
		fmt.Sprintf("State:      %s", state),
		fmt.Sprintf("Child PID:  %s", pid),
		fmt.Sprintf("Child up:   %s", doc.ChildUptime),
		fmt.Sprintf("Session expires: %s", doc.SessionExpiresAt),
		fmt.Sprintf("Next refresh:    %s", doc.RefreshWindowNext),
		fmt.Sprintf("Next reseal:     %s", resealNext),
		fmt.Sprintf("Healthy scopes:  %s", healthy),
		fmt.Sprintf("Stale scopes:    %s", stale),
		fmt.Sprintf("Discord:    %s", discord),
		fmt.Sprintf("Last auth fail:  %s", authFail),
	}
	if _, err := io.WriteString(w, strings.Join(lines, "\n")+"\n"); err != nil {
		return fmt.Errorf("hush: client status: write: %w", err)
	}
	return nil
}

// joinScopes renders a scope list as "a, b, c" or "(none)" when empty.
func joinScopes(s []string) string {
	if len(s) == 0 {
		return "(none)"
	}
	return strings.Join(s, ", ")
}

// printClientErr writes err to stderr in the locked
// `hush: client <verb>: <msg>` shape. Newlines in the message are
// replaced with spaces so the line stays one-line.
func printClientErr(stderr io.Writer, verb string, err error) {
	if err == nil {
		return
	}
	msg := strings.ReplaceAll(err.Error(), "\n", " ")
	_, _ = fmt.Fprintf(stderr, "hush: client %s: %s\n", verb, msg)
}

// stdoutFile extracts the *os.File when w is an *os.File. Returns
// (nil, false) when w is wrapped (test buffers, bytes.Buffer, etc.).
func stdoutFile(w io.Writer) (*os.File, bool) {
	if f, ok := w.(*os.File); ok {
		return f, true
	}
	return nil, false
}
