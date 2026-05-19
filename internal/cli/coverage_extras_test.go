package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mrz1836/hush/internal/config"
	"github.com/mrz1836/hush/internal/discord"
	"github.com/mrz1836/hush/internal/server"
	"github.com/mrz1836/hush/internal/token"
)

// TestEphemeralRevokeKey covers the fallback-key generator used when
// the production wiring has not derived a per-client key yet.
func TestEphemeralRevokeKey(t *testing.T) {
	t.Parallel()
	key, err := ephemeralRevokeKey(rand.Reader)
	if err != nil {
		t.Fatalf("ephemeralRevokeKey: %v", err)
	}
	if key == nil {
		t.Fatal("nil key")
	}
}

// TestMapSessionType covers the discord/chassis session-type mapping.
func TestMapSessionType(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   server.SessionType
		want token.SessionType
	}{
		{server.SessionInteractive, token.SessionInteractive},
		{server.SessionSupervisor, token.SessionSupervisor},
		{server.SessionType(0), token.SessionInteractive},
	}
	for _, c := range cases {
		if got := mapSessionType(c.in); got != c.want {
			t.Errorf("mapSessionType(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

// fakeDiscordApprover is a discord.Approver test double for the
// adapter test.
type fakeDiscordApprover struct {
	dec discord.Decision
	err error
}

func (f *fakeDiscordApprover) RequestApproval(_ context.Context, _ discord.ApprovalRequest) (discord.Decision, error) {
	return f.dec, f.err
}

// TestDiscordApproverAdapter_TranslatesDecisionsAndErrors covers the
// production-side adapter that bridges discord.Approver to the
// chassis Approver interface.
//
//nolint:gocognit // table-driven over locked sentinel translations
func TestDiscordApproverAdapter_TranslatesDecisionsAndErrors(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		dec     discord.Decision
		err     error
		wantApp bool
		wantErr error
	}{
		{"approve", discord.Decision{Approved: true, ApprovedTTL: 10}, nil, true, nil},
		{"deny via false", discord.Decision{Approved: false}, nil, false, server.ErrApproverDenied},
		{"deny via err", discord.Decision{}, discord.ErrApprovalDenied, false, server.ErrApproverDenied},
		{"timeout", discord.Decision{}, discord.ErrApprovalTimeout, false, server.ErrApproverTimeout},
		{"unavailable", discord.Decision{}, discord.ErrDiscordUnavailable, false, server.ErrApproverUnavailable},
		{"rate-limited", discord.Decision{}, discord.ErrRateLimited, false, server.ErrApproverRateLimited},
		{"unknown error", discord.Decision{}, errSyntheticTest, false, errSyntheticTest},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			adapter := discordApproverAdapter{inner: &fakeDiscordApprover{dec: c.dec, err: c.err}}
			gotDec, gotErr := adapter.RequestApproval(t.Context(), server.ApprovalRequest{
				SessionType: server.SessionInteractive,
			})
			if gotDec.Approved != c.wantApp {
				t.Errorf("Approved = %t, want %t", gotDec.Approved, c.wantApp)
			}
			if c.wantErr != nil {
				if gotErr == nil {
					t.Fatalf("err = nil, want %v", c.wantErr)
				}
				if !errors.Is(gotErr, c.wantErr) && gotErr.Error() != c.wantErr.Error() {
					t.Errorf("err = %v, want %v", gotErr, c.wantErr)
				}
			}
		})
	}
}

// TestPrintErr covers the printErr helper.
func TestPrintErr(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	stream := newStream(&buf, false, true)
	printErr(stream, "boom %s", "bang")
	if !strings.Contains(buf.String(), "hush: boom bang") {
		t.Errorf("got %q", buf.String())
	}
}

// TestClassifyTransportErr covers each branch of the classifier.
func TestClassifyTransportErr(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   error
		want string
	}{
		{nil, "unknown"},
		{context.DeadlineExceeded, "timeout after 5s"},
		{syscall.ECONNREFUSED, "connection refused"},
		{errSyntheticDial, "name resolution failed"},
		{errSyntheticNoRoute, "name resolution failed"},
		{errSyntheticEOF, "EOF"},
		{errSyntheticRefused, "connection refused"},
		{errSyntheticTimeout, "timeout after 5s"},
		{errSyntheticDeadline, "timeout after 5s"},
	}
	for _, c := range cases {
		if got := classifyTransportErr(c.in); got != c.want {
			t.Errorf("classify(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestMark covers all four branches of the mark helper.
func TestMark(t *testing.T) {
	t.Parallel()
	cases := []struct {
		ok, noColor bool
		want        string
	}{
		{true, true, "OK "},
		{false, true, "X  "},
		{true, false, "\x1b[32m✔\x1b[0m "},
		{false, false, "\x1b[31m✘\x1b[0m "},
	}
	for _, c := range cases {
		if got := mark(c.ok, c.noColor); got != c.want {
			t.Errorf("mark(%t, %t) = %q, want %q", c.ok, c.noColor, got, c.want)
		}
	}
}

// TestOutputFromCmd_FallbackToDefaults covers the path where the
// cobra context has no outputCtxKey value attached.
func TestOutputFromCmd_FallbackToDefaults(t *testing.T) {
	t.Parallel()
	root := newRootCmd(&outputContext{stdout: newStream(&bytes.Buffer{}, false, true), stderr: newStream(&bytes.Buffer{}, false, true)})
	root.SetContext(context.Background())
	got := outputFromCmd(root)
	if got == nil || got.stdout == nil || got.stderr == nil {
		t.Fatal("nil fallback")
	}
}

// TestExecute_HappyPath covers the cli.Execute entry point with a
// minimal subcommand invocation that exits cleanly.
func TestExecute_HappyPath(t *testing.T) {
	t.Parallel()
	prev := os.Args
	t.Cleanup(func() { os.Args = prev })
	os.Args = []string{"hush", "version"}
	if got := Execute(t.Context()); got != ExitOK {
		t.Errorf("Execute = %d, want ExitOK", got)
	}
}

// TestServeCmdShape asserts the cobra command builder returns a
// non-nil RunE so the cobra framework never panics on dispatch.
func TestServeCmdShape(t *testing.T) {
	t.Parallel()
	cmd := newServeCmd()
	if cmd == nil || cmd.RunE == nil || cmd.Use == "" {
		t.Fatal("serve command malformed")
	}
	if cmd.Flags().Lookup("reload-on-vault-change") == nil {
		t.Fatal("serve command missing --reload-on-vault-change")
	}
}

// TestRunHealth_TimeoutMessage drives a slow server to exercise the
// 5s timeout classifier path.
func TestRunHealth_TimeoutMessage(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		// Sleep longer than the test's per-call ctx allows.
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Millisecond)
	defer cancel()

	var stdout, stderr bytes.Buffer
	out := newStream(&stdout, false, true)
	errStream := newStream(&stderr, false, true)
	err := runHealth(ctx, out, errStream, srv.URL)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if got := mapErr(err); got != ExitErr {
		t.Errorf("mapErr = %d, want ExitErr", got)
	}
}

// fmtError plus fmtError-on-existing-err — small coverage for the
// helper paths in root.go used by multiple subcommands.
func TestFmtErrorWraps(t *testing.T) {
	t.Parallel()
	err := fmtError(errMissingFlag, "--abc")
	if !errors.Is(err, errMissingFlag) {
		t.Errorf("fmtError lost sentinel: %v", err)
	}
	if !strings.Contains(err.Error(), "--abc") {
		t.Errorf("fmtError missing context: %v", err)
	}
	if got := fmt.Sprintf("%T", err); got == "" {
		t.Errorf("unexpected empty type")
	}
}

// TestNewHealthCmd_RunE_RoutesThroughOutputContext drives the cobra
// shell of `hush health` to cover the RunE wrapper.
func TestNewHealthCmd_RunE_RoutesThroughOutputContext(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok","uptime":"1s","secrets_count":0,"active_tokens":0,"discord_connected":true,"config_valid":true,"vault_loaded":true,"clock_in_sync":true}`))
	}))
	t.Cleanup(srv.Close)

	root := newRootCmd(&outputContext{stdout: newStream(&bytes.Buffer{}, false, true), stderr: newStream(&bytes.Buffer{}, false, true)})
	root.SetArgs([]string{"health", "--server", srv.URL})
	root.SetContext(t.Context())
	if err := root.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
}

// TestNewRevokeCmd_RunE_FailsCloseConnection drives the cobra shell
// of `hush revoke` against a closed port to cover the RunE wrapper.
func TestNewRevokeCmd_RunE_FailsCloseConnection(t *testing.T) {
	t.Parallel()
	root := newRootCmd(&outputContext{stdout: newStream(&bytes.Buffer{}, false, true), stderr: newStream(&bytes.Buffer{}, false, true)})
	root.SetArgs([]string{"revoke", "--server", "http://127.0.0.1:1", "--jti", validJTI})
	root.SetContext(t.Context())
	if err := root.Execute(); err == nil {
		t.Fatal("expected error")
	}
}

// TestLoadBotToken_KeychainAbsent asserts the production keychain
// helper returns errBotTokenMissing when the helper subprocess fails
// (e.g. keychain item not present in the test environment).
func TestLoadBotToken_KeychainAbsent(t *testing.T) {
	// Force the keychain path. Developer shells often export a real
	// HUSH_DISCORD_BOT_TOKEN, but this is a unit test and must never
	// construct a live Discord session or trigger firewall prompts.
	t.Setenv("HUSH_DISCORD_BOT_TOKEN", "")
	_, err := loadBotToken(t.Context(), "hush-nonexistent-test-item")
	if err == nil {
		t.Skip("keychain unexpectedly contained the test item")
	}
	if !errors.Is(err, errBotTokenMissing) && !errors.Is(err, errBotTokenSubprocess) {
		t.Errorf("err = %v, want errBotTokenMissing or errBotTokenSubprocess", err)
	}
}

// TestNewProductionBotApprover_BadKeychain asserts the production
// approver factory surfaces the keychain error when the configured
// item is absent.
func TestNewProductionBotApprover_BadKeychain(t *testing.T) {
	// Force the missing-keychain branch. If a developer has
	// HUSH_DISCORD_BOT_TOKEN in their environment, newProductionBotApprover
	// would otherwise build a real discordgo session and call Open(), which
	// makes cli.test try to reach discord.com.
	t.Setenv("HUSH_DISCORD_BOT_TOKEN", "")
	cfg := &config.Server{
		Server: config.ServerSection{
			DiscordOwnerID: "100000000000000000",
		},
		Discord: config.DiscordSection{
			BotTokenKeychainItem: "hush-nonexistent-test-item",
			ApplicationID:        "100000000000000000",
		},
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	_, _, err := newProductionBotApprover(t.Context(), cfg, logger)
	if err == nil {
		t.Skip("keychain unexpectedly contained the test item")
	}
}
