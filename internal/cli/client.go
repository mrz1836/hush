package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/mrz1836/hush/internal/supervise"
)

// statusDoc mirrors the SDD-22 statusJSON shape one-to-one. Used for
// human-rendering on TTY paths. The pipe / `--json` path writes the
// raw socket bytes verbatim (R-5).
type statusDoc struct {
	Supervisor        string   `json:"supervisor"`
	State             string   `json:"state"`
	SessionExpiresAt  string   `json:"session_expires_at"`
	RefreshWindowNext string   `json:"refresh_window_next"`
	ScopeHealthy      []string `json:"scope_healthy"`
	ScopeStale        []string `json:"scope_stale"`
	LastAuthFailure   *string  `json:"last_auth_failure"`
	ChildPID          *int     `json:"child_pid"`
	ChildUptime       string   `json:"child_uptime"`
	DiscordConnected  bool     `json:"discord_connected"`
}

// refreshAck is the terminal refresh-verb response shape.
type refreshAck struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// clientStatusTimeout is the FR-023-19 wall-clock ceiling on `client
// status`. Overridable from test code via the test-only seam below.
//
//nolint:gochecknoglobals // sentinel-class timeout knob; mutated only by tests via withTimeouts.
var clientStatusTimeout = 2 * time.Second

// clientRefreshTimeout is the FR-023-24 wall-clock ceiling on `client
// refresh`. Overridable from test code via the test-only seam below.
//
//nolint:gochecknoglobals // sentinel-class timeout knob; mutated only by tests via withTimeouts.
var clientRefreshTimeout = 90 * time.Second

// isTerminalFn is the TTY-detect seam. Production wires
// term.IsTerminal; tests override to force one branch or the other
// (R-5).
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
// --json flag — refresh has no format option (FR-023-17a).
func newClientRefreshCmd() *cobra.Command {
	flags := clientRefreshFlags{}
	cmd := &cobra.Command{
		Use:   "refresh",
		Short: "Trigger an immediate refresh on a running supervisor",
		Args:  cobra.NoArgs,
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

// resolveSocketPath applies the FR-023-15 precedence rule:
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

// runClientStatus dials the supervisor socket, sends "status\n", reads
// the response, and dispatches to the human-text or raw-JSON output
// path per the TTY / --json decision (cli-client.md §2.4 / §2.5).
func runClientStatus(cmd *cobra.Command, flags clientStatusFlags) error {
	stderr := cmd.ErrOrStderr()

	path, err := resolveSocketPath(flags.socketPath, flags.supervisorName)
	if err != nil {
		printClientErr(stderr, "status", err)
		return err
	}

	ctx, cancel := context.WithTimeout(cmd.Context(), clientStatusTimeout)
	defer cancel()

	body, err := unixRoundTrip(ctx, path, "status\n")
	if err != nil {
		wrapped := fmt.Errorf("%w: %w", errSocketUnreachable, err)
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
		body = ensureSingleTrailingNewline(body)
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

	body, err := unixRoundTrip(ctx, path, "refresh\n")
	if err != nil {
		wrapped := fmt.Errorf("%w: %w", errSocketUnreachable, err)
		printClientErr(stderr, "refresh", wrapped)
		return wrapped
	}
	var ack refreshAck
	if jerr := json.Unmarshal(bytes.TrimSpace(body), &ack); jerr != nil {
		wrapped := fmt.Errorf("%w: parse ack: %w", errSocketUnreachable, jerr)
		printClientErr(stderr, "refresh", wrapped)
		return wrapped
	}
	if ack.OK {
		return nil
	}
	wrapped := fmt.Errorf("%w: %s", errSupervisorRefused, ack.Error)
	printClientErr(stderr, "refresh", wrapped)
	return wrapped
}

// unixRoundTrip dials a Unix socket at path, writes verb, reads the
// full response until EOF or the ctx deadline, and returns the bytes
// read. Single attempt — never retries (FR-023-19 / FR-023-24
// anti-contract).
func unixRoundTrip(ctx context.Context, path, verb string) ([]byte, error) {
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", path)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", path, err)
	}
	defer func() { _ = conn.Close() }()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if _, werr := conn.Write([]byte(verb)); werr != nil {
		return nil, fmt.Errorf("write verb: %w", werr)
	}
	body, rerr := io.ReadAll(conn)
	if rerr != nil && !errors.Is(rerr, io.EOF) {
		return nil, fmt.Errorf("read response: %w", rerr)
	}
	return body, nil
}

// ensureSingleTrailingNewline returns body with exactly one trailing
// '\n' — adds one when absent, trims duplicates when present.
func ensureSingleTrailingNewline(body []byte) []byte {
	body = bytes.TrimRight(body, "\n")
	body = append(body, '\n')
	return body
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
