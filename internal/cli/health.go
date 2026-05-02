package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

// errPartialHealth is the sentinel returned when /hz reports green
// transport but at least one dimension is unhealthy. Mapped to
// ExitErr by mapErr (FR-017a).
var errPartialHealth = errors.New("partial-health")

// errHealthServer is the sentinel for /hz HTTP responses outside
// the 200 OK / partial-health path (e.g. 5xx, malformed body).
var errHealthServer = errors.New("server returned non-OK status")

// errHealthDecode is the sentinel for a malformed /hz response body.
var errHealthDecode = errors.New("malformed health response")

// healthSnapshot mirrors the chassis's GET /hz response shape
// (data-model.md §6). Eight keys, exact order locked.
type healthSnapshot struct {
	Status           string `json:"status"`
	Uptime           string `json:"uptime"`
	SecretsCount     int    `json:"secrets_count"`
	ActiveTokens     int    `json:"active_tokens"`
	DiscordConnected bool   `json:"discord_connected"`
	ConfigValid      bool   `json:"config_valid"`
	VaultLoaded      bool   `json:"vault_loaded"`
	ClockInSync      bool   `json:"clock_in_sync"`
}

// healthTotalTimeout is the locked total-request timeout for `hush
// health` (FR-015a). Operators see a single number to reason about,
// no per-phase split.
const healthTotalTimeout = 5 * time.Second

func newHealthCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "health",
		Short: "Check the health of the hush server",
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := outputFromCmd(cmd)
			server, _ := cmd.Flags().GetString("server")
			return runHealth(cmd.Context(), out.stdout, out.stderr, server)
		},
	}
	cmd.Flags().String("server", "", "Server URL (e.g. http://100.x.y.z:7743/h/<prefix>); required when no --config is loaded")
	return cmd
}

// runHealth issues a single bounded GET <server>/hz, classifies any
// transport failure into the locked literal-text contract, and prints
// the per-dimension summary (TTY) or raw JSON (pipe). Partial-health
// returns ExitErr while still printing the summary (FR-017a).
//
//nolint:gocognit,cyclop,gocyclo // sequential request→classify→render pipeline; branches map 1:1 to documented failure modes
func runHealth(ctx context.Context, stdout, stderr *Stream, serverURL string) error {
	if serverURL == "" {
		_ = stderr.WriteText("hush: --server is required (auto-discovery from --config arrives in a later chunk)")
		return fmtError(errMissingFlag, "--server")
	}
	target := strings.TrimRight(serverURL, "/") + "/hz"

	client := &http.Client{
		Timeout: healthTotalTimeout,
		Transport: &http.Transport{
			DisableKeepAlives:   true,
			MaxIdleConnsPerHost: 1,
		},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		_ = stderr.WriteText("could not connect to hush server at %s: %s", serverURL, classifyTransportErr(err))
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		_ = stderr.WriteText("could not connect to hush server at %s: %s", serverURL, classifyTransportErr(err))
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 500 {
		_ = stderr.WriteText("server returned %d at %s", resp.StatusCode, serverURL)
		return fmt.Errorf("hush/cli: health: %w (status=%d)", errHealthServer, resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		_ = stderr.WriteText("server returned %d at %s", resp.StatusCode, serverURL)
		return fmt.Errorf("hush/cli: health: %w (status=%d)", errHealthServer, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		_ = stderr.WriteText("could not connect to hush server at %s: %s", serverURL, classifyTransportErr(err))
		return err
	}

	var snap healthSnapshot
	if jerr := json.Unmarshal(body, &snap); jerr != nil {
		_ = stderr.WriteText("server returned malformed health response at %s", serverURL)
		return fmt.Errorf("hush/cli: health: %w: %w", errHealthDecode, jerr)
	}

	if stdout.IsTTY() {
		if err := stdout.WriteText("%s", renderHealthText(snap, stdout.noColor)); err != nil {
			return err
		}
	} else {
		// Echo the server's body byte-for-byte (FR-013, contract §5.2).
		body = append(body, '\n')
		if _, werr := stdout.w.Write(body); werr != nil {
			return werr
		}
	}

	if !healthIsAllGreen(snap) {
		return errPartialHealth
	}
	return nil
}

// healthIsAllGreen reports whether every health dimension is green
// (FR-017a). SecretsCount and ActiveTokens are informational, not
// gates.
func healthIsAllGreen(s healthSnapshot) bool {
	return s.Status == "ok" &&
		s.DiscordConnected &&
		s.ConfigValid &&
		s.VaultLoaded &&
		s.ClockInSync
}

// renderHealthText returns the TTY-mode two-column summary in stable
// JSON-key order. Healthy rows render with a green checkmark; failing
// rows with a red cross — both suppressed when noColor is true.
func renderHealthText(s healthSnapshot, noColor bool) string {
	var b strings.Builder
	row := func(name string, ok bool, value string) {
		mark := mark(ok, noColor)
		fmt.Fprintf(&b, "%s %-20s %s\n", mark, name, value)
	}
	row("status", s.Status == "ok", s.Status)
	row("uptime", true, s.Uptime)
	fmt.Fprintf(&b, "  %-20s %d\n", "secrets_count", s.SecretsCount)
	fmt.Fprintf(&b, "  %-20s %d\n", "active_tokens", s.ActiveTokens)
	row("discord_connected", s.DiscordConnected, fmt.Sprintf("%t", s.DiscordConnected))
	row("config_valid", s.ConfigValid, fmt.Sprintf("%t", s.ConfigValid))
	row("vault_loaded", s.VaultLoaded, fmt.Sprintf("%t", s.VaultLoaded))
	row("clock_in_sync", s.ClockInSync, fmt.Sprintf("%t", s.ClockInSync))
	return strings.TrimRight(b.String(), "\n")
}

// mark renders a per-dimension status marker. Returns "" when noColor
// is true and the dimension is healthy (so the column stays in
// alignment); returns the literal "FAIL" prefix on the unhealthy
// path even under --no-color so failures stay visible in colorless
// logs.
func mark(ok, noColor bool) string {
	switch {
	case ok && noColor:
		return "OK "
	case !ok && noColor:
		return "X  "
	case ok:
		return "\x1b[32m✔\x1b[0m "
	default:
		return "\x1b[31m✘\x1b[0m "
	}
}

// classifyTransportErr maps a transport error to one of the
// contract-locked classifier strings used by the literal stderr
// messages on health and revoke (FR-014, FR-015).
func classifyTransportErr(err error) string {
	if err == nil {
		return "unknown"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout after 5s"
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return "connection refused"
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "no such host"), strings.Contains(msg, "no route"):
		return "name resolution failed"
	case strings.Contains(msg, "EOF"):
		return "EOF"
	case strings.Contains(msg, "connection refused"):
		return "connection refused"
	case strings.Contains(msg, "Client.Timeout"), strings.Contains(msg, "context deadline"):
		return "timeout after 5s"
	}
	return msg
}
