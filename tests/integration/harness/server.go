//go:build integration

package harness

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mrz1836/hush/internal/audit"
	"github.com/mrz1836/hush/internal/config"
	"github.com/mrz1836/hush/internal/keys"
	"github.com/mrz1836/hush/internal/server"
	"github.com/mrz1836/hush/internal/testutil"
	"github.com/mrz1836/hush/internal/token"
	"github.com/mrz1836/hush/internal/vault"
)

// TestServer composes the REAL internal/server chassis over a
// loopback TCP listener. Only four boundaries are mocked: Discord
// (TestDiscord.AsApprover), validator HTTP (via
// MockValidator), wall clock (FakeClock injected by the supervisor), and
// the Tailscale-bind probe (faked via Deps.InterfaceLister to claim the
// configured CGNAT address is bound locally).
//
// Audit, token store, vault, and HTTP transport are real production code.
type TestServer struct {
	vault       *TestVault
	url         string
	listener    net.Listener
	auditWriter audit.Writer
	auditCancel context.CancelFunc
	auditDone   chan struct{}
	srv         *server.Server
	runCancel   context.CancelFunc
	runDone     chan struct{}

	tokenStore  token.Store
	vaultPtr    *atomic.Pointer[vault.Store]
	auditPubKey *ecdsa.PublicKey

	jtiMu      sync.Mutex
	issuedJTIs []string

	validatorsMu sync.Mutex
	validators   map[string]*validatorRoute
}

// validatorRoute holds the per-scope httptest server registered via
// MockValidator. The supervisor (not the server) calls these endpoints
// when running validator checks; they live under the harness's allow-list.
type validatorRoute struct {
	server *httptestServer
}

// httptestServer mirrors httptest.Server so we can register the URL with
// the allow-list at construction time and tear it down via t.Cleanup.
type httptestServer struct {
	srv    *http.Server
	listen net.Listener
}

// ServerOpts configures NewServer. Logger and Discord are required when
// scenarios need a working /claim flow.
type ServerOpts struct {
	Vault   *TestVault
	Logger  *LogCapture
	Discord *TestDiscord
}

// NewServer composes the real internal/server chassis bound to
// 127.0.0.1:<ephemeral> and returns a TestServer. Cleanup is registered
// via t.Cleanup. If Discord is nil, the server's Approver is wired to
// always return ErrApproverUnavailable.
//
//nolint:funlen,cyclop // sequential composition; each step delegates to internal/server
func NewServer(t *testing.T, opts ServerOpts) *TestServer {
	t.Helper()
	if opts.Vault == nil {
		t.Fatal("harness.NewServer: Vault is required")
	}

	// 1. Bind a loopback listener and harvest its address.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("harness.NewServer: listen: %v", err)
	}
	listenerAddr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		_ = listener.Close()
		t.Fatalf("harness.NewServer: listener.Addr is %T, want *net.TCPAddr", listener.Addr())
	}

	cfg := newTestServerConfig(t, opts.Vault, listenerAddr)

	// 2. Set up the real audit writer; its Run goroutine outlives the
	// chassis ctx so the drain step in writerImpl.Run completes before
	// shutdown. Pattern mirrors internal/cli/serve.go:229.
	auditKey := server_test_audit_key(t)
	logger := opts.Logger.Logger()
	auditWriter, err := audit.NewWriter(t.Context(), opts.Vault.AuditPath(), auditKey, nil, logger)
	if err != nil {
		_ = listener.Close()
		t.Fatalf("harness.NewServer: audit.NewWriter: %v", err)
	}
	auditCtx, auditCancel := context.WithCancel(context.Background())
	auditDone := make(chan struct{})
	go func() {
		defer close(auditDone)
		_ = auditWriter.Run(auditCtx)
	}()

	// 3. Load the vault.Store and wrap it in atomic.Pointer for SIGHUP.
	store, err := vault.Load(t.Context(), opts.Vault.Path(), opts.Vault.Key())
	if err != nil {
		auditCancel()
		<-auditDone
		_ = listener.Close()
		t.Fatalf("harness.NewServer: vault.Load: %v", err)
	}
	var vptr atomic.Pointer[vault.Store]
	vptr.Store(&store)

	// 4. Derive the JWT signing key from the deterministic test seed.
	seed := testutil.NewTestKeys(t)
	jwtKey, err := keys.DeriveJWTSigningKey(seed)
	if err != nil {
		auditCancel()
		<-auditDone
		_ = listener.Close()
		t.Fatalf("harness.NewServer: DeriveJWTSigningKey: %v", err)
	}

	// 5. Build the chassis Deps and wire the Discord adapter.
	var approver server.Approver
	var discordHealth func() bool
	if opts.Discord != nil {
		approver = opts.Discord.AsApprover()
		discordHealth = opts.Discord.Connected
	} else {
		approver = unavailableApprover{}
		discordHealth = func() bool { return false }
	}

	tokenStore := token.NewStore()
	ts := &TestServer{}
	deps := server.Deps{
		Cfg:        cfg,
		VaultPtr:   &vptr,
		TokenStore: tokenStore,
		TokenIssuer: func(ctx context.Context, params token.IssueParams) (*token.Token, error) {
			tok, issErr := token.Issue(ctx, jwtKey, params)
			if issErr == nil && tok != nil {
				ts.recordJTI(tok.JTI)
			}
			return tok, issErr
		},
		Approver:        approver,
		Logger:          logger,
		AuditWriter:     server.NewChassisAuditAdapter(auditWriter),
		JWTVerifyKey:    &jwtKey.PublicKey,
		DiscordHealth:   discordHealth,
		Listener:        listener,
		VaultKey:        opts.Vault.Key(),
		ClockSyncProbe:  alwaysSyncedClock,
		InterfaceLister: fakeCGNATInterfaceLister(cfg.Server.ListenAddr.Addr()),
	}

	srv, err := server.New(deps)
	if err != nil {
		auditCancel()
		<-auditDone
		_ = listener.Close()
		t.Fatalf("harness.NewServer: server.New: %v", err)
	}
	if err := srv.RegisterHandlers(); err != nil {
		auditCancel()
		<-auditDone
		_ = listener.Close()
		t.Fatalf("harness.NewServer: RegisterHandlers: %v", err)
	}

	runCtx, runCancel := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		_ = srv.Run(runCtx)
	}()

	url := fmt.Sprintf("http://%s/h/%s", listenerAddr.String(), cfg.Server.PathPrefix)
	registerAllowedHostExternal(url)

	*ts = TestServer{
		vault:       opts.Vault,
		url:         url,
		listener:    listener,
		auditWriter: auditWriter,
		auditCancel: auditCancel,
		auditDone:   auditDone,
		srv:         srv,
		runCancel:   runCancel,
		runDone:     runDone,
		tokenStore:  tokenStore,
		vaultPtr:    &vptr,
		auditPubKey: &auditKey.PublicKey,
		validators:  make(map[string]*validatorRoute),
	}
	t.Cleanup(ts.Stop)
	// Probe the /hz endpoint to confirm the chassis is serving before
	// returning; bounded poll, no time.Sleep.
	ts.waitReady(t)
	return ts
}

// waitReady polls GET /hz until 2xx or the deadline fires.
func (s *TestServer) waitReady(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	client := &http.Client{Timeout: 200 * time.Millisecond}
	for time.Now().Before(deadline) {
		resp, err := client.Get(s.url + "/hz") //nolint:noctx // bounded poll inside harness setup
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode < 500 {
				return
			}
		}
	}
	t.Fatalf("harness.NewServer: server did not become ready at %s within 2s", s.url)
}

// URL returns the chassis base URL including /h/<prefix>. Supervisor and
// client configs point their server_url field here.
func (s *TestServer) URL() string { return s.url }

// Vault returns the harness vault wired into this server.
func (s *TestServer) Vault() *TestVault { return s.vault }

// TokenStore exposes the live in-memory token store for assertions.
func (s *TestServer) TokenStore() token.Store { return s.tokenStore }

// FlushSessions revokes every session JTI the server has issued, simulating
// a vault-server restart that loses its in-memory active-session map
// (docs §7). After FlushSessions every previously issued JWT fails the
// /s/<name> bearer check with an unknown-jti rejection.
func (s *TestServer) FlushSessions() {
	s.jtiMu.Lock()
	jtis := append([]string(nil), s.issuedJTIs...)
	s.jtiMu.Unlock()
	for _, jti := range jtis {
		_, _ = s.tokenStore.RevokeIdempotent(jti)
	}
}

// Reload performs a SIGHUP-equivalent atomic vault reload (Scenario 13): it
// re-opens the vault file from disk and swaps the server's atomic vault
// pointer — the same seam production wires to SIGHUP. Pair with
// TestVault.Rotate to propagate a rotated secret to a running server.
func (s *TestServer) Reload(ctx context.Context) error {
	store, err := vault.Load(ctx, s.vault.Path(), s.vault.Key())
	if err != nil {
		return fmt.Errorf("harness: Reload: vault.Load: %w", err)
	}
	s.vaultPtr.Store(&store)
	return nil
}

// AuditKey returns the secp256k1 public key the server audit chain is
// signed with.
func (s *TestServer) AuditKey() *ecdsa.PublicKey { return s.auditPubKey }

// AssertAuditChain stops the server (draining the audit writer) and verifies
// the on-disk server audit chain is hash-linked and signature-valid. Call
// once at scenario end; Stop is idempotent so the t.Cleanup-registered Stop
// stays a harmless no-op afterwards.
func (s *TestServer) AssertAuditChain(t *testing.T) {
	t.Helper()
	s.Stop()
	AssertAuditChainContinuity(t, s.vault.AuditPath(), s.auditPubKey)
}

// ReadAudit parses the audit JSONL into a slice of events. Returns an
// empty slice if the file does not exist yet.
func (s *TestServer) ReadAudit() []audit.Event {
	raw := s.RawAudit()
	if len(raw) == 0 {
		return nil
	}
	var out []audit.Event
	for _, line := range splitLines(raw) {
		if len(line) == 0 {
			continue
		}
		var ev audit.Event
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		out = append(out, ev)
	}
	return out
}

// RawAudit returns the raw audit-log byte stream for the sentinel sweep.
func (s *TestServer) RawAudit() []byte {
	if s.vault == nil {
		return nil
	}
	f, err := os.Open(s.vault.AuditPath())
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	out, _ := io.ReadAll(f)
	return out
}

// MockValidator registers an httptest server for the named scope. The
// supervisor's validator pool routes by scope name; the URL of the
// registered httptest server is returned for the supervisor to dial.
// Each route registered here is also added to the suite-wide allow-list.
func (s *TestServer) MockValidator(t *testing.T, scope string, handler http.HandlerFunc) string {
	t.Helper()
	s.validatorsMu.Lock()
	defer s.validatorsMu.Unlock()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("harness.MockValidator: listen: %v", err)
	}
	httpSrv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 2 * time.Second,
	}
	go func() { _ = httpSrv.Serve(listener) }()
	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		_ = listener.Close()
		t.Fatalf("harness.MockValidator: listener.Addr is %T", listener.Addr())
	}
	url := fmt.Sprintf("http://%s", addr.String())
	registerAllowedHostExternal(url)
	s.validators[scope] = &validatorRoute{
		server: &httptestServer{srv: httpSrv, listen: listener},
	}
	t.Cleanup(func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutCtx)
	})
	return url
}

// ValidatorURL returns the registered URL for the named scope, or empty.
func (s *TestServer) ValidatorURL(scope string) string {
	s.validatorsMu.Lock()
	defer s.validatorsMu.Unlock()
	r, ok := s.validators[scope]
	if !ok || r == nil || r.server == nil || r.server.listen == nil {
		return ""
	}
	addr, ok := r.server.listen.Addr().(*net.TCPAddr)
	if !ok {
		return ""
	}
	return fmt.Sprintf("http://%s", addr.String())
}

// Stop cancels the running chassis and drains the audit writer. Idempotent.
// Registered via t.Cleanup in NewServer; callers rarely invoke directly.
func (s *TestServer) Stop() {
	if s == nil {
		return
	}
	if s.runCancel != nil {
		s.runCancel()
		<-s.runDone
		s.runCancel = nil
	}
	if s.auditCancel != nil {
		s.auditCancel()
		<-s.auditDone
		s.auditCancel = nil
	}
}

// recordJTI appends a freshly issued session JTI to the harness ledger so
// FlushSessions can later revoke every live session.
func (s *TestServer) recordJTI(jti string) {
	if jti == "" {
		return
	}
	s.jtiMu.Lock()
	s.issuedJTIs = append(s.issuedJTIs, jti)
	s.jtiMu.Unlock()
}

// alwaysSyncedClock reports clock-synced=true with zero drift. Used as
// the test seam for Deps.ClockSyncProbe.
func alwaysSyncedClock(_ context.Context) (bool, time.Duration, error) {
	return true, 0, nil
}

// fakeCGNATInterfaceLister returns a closure that reports `bind` as a
// locally bound interface address. Satisfies the tailscale_bind startup
// check without requiring the host to actually own a CGNAT IP.
func fakeCGNATInterfaceLister(bind netip.Addr) func() ([]net.Addr, error) {
	return func() ([]net.Addr, error) {
		ip := net.ParseIP(bind.String())
		if ip == nil {
			return nil, fmt.Errorf("harness: invalid bind addr %q", bind)
		}
		return []net.Addr{&net.IPNet{IP: ip, Mask: net.CIDRMask(32, 32)}}, nil
	}
}

// unavailableApprover is the fallback Approver wired when ServerOpts has
// no Discord adapter. It always returns ErrApproverUnavailable so the
// /claim path fails closed rather than panicking.
type unavailableApprover struct{}

// RequestApproval satisfies server.Approver.
func (unavailableApprover) RequestApproval(_ context.Context, _ server.ApprovalRequest) (server.Decision, error) {
	return server.Decision{}, server.ErrApproverUnavailable
}

// server_test_audit_key derives a deterministic secp256k1 key for the
// audit writer's signing chain. Mirrors the production keychain-derived
// key but uses the test seed so audit.Verify can replay deterministically.
func server_test_audit_key(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	seed := testutil.NewTestKeys(t)
	k, err := keys.DeriveJWTSigningKey(seed) // any deterministic key works for chain signing
	if err != nil {
		t.Fatalf("harness: derive audit key: %v", err)
	}
	return k
}

// splitLines is a minimal JSONL splitter that tolerates a trailing
// newline. Returned slices share storage with raw.
func splitLines(raw []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, b := range raw {
		if b == '\n' {
			out = append(out, raw[start:i])
			start = i + 1
		}
	}
	if start < len(raw) {
		out = append(out, raw[start:])
	}
	return out
}

// registerAllowedHostExternal forwards to the lifecycle_test.go suite
// allow-list. Defined as a method-pointer seam so harness code can
// register listeners without importing the test package directly.
//
//nolint:gochecknoglobals // suite seam: replaced once by RegisterAllowedHostHook at TestMain init
var registerAllowedHostExternal = func(_ string) {
	// no-op default; integration suite's TestMain installs the real one
}

// RegisterAllowedHostHook is called once at suite init to bridge harness
// listener registrations into the suite's process-wide RoundTripper allow-list.
func RegisterAllowedHostHook(fn func(string)) {
	registerAllowedHostExternal = fn
}

// newTestServerConfig writes a minimal valid server TOML to the vault's
// state directory and loads it via the real config.LoadServer pipeline.
// The listen_addr is a CGNAT placeholder (100.96.10.4) that satisfies
// the Tailscale-bind validator; the actual listener is supplied via
// Deps.Listener and binds to 127.0.0.1:<ephemeral>. The InterfaceLister
// seam faked in NewServer reports the CGNAT addr as locally bound so
// the startup tailscale_bind check passes.
//
// File-mode + NTP-sync checks are disabled — the test fixture chmods
// the state dir to 0700 and creates files at 0600 already, but the
// audit.jsonl appearing partway through startup races the walk.
func newTestServerConfig(t *testing.T, v *TestVault, _ *net.TCPAddr) *config.Server {
	t.Helper()
	tomlBody := fmt.Sprintf(`[server]
listen_addr = "100.96.10.4:7743"
path_prefix = "harness1"
state_dir = %q
audit_log = %q
discord_owner_id = "123456789012345678"
client_registry = %q

[discord]
bot_token_keychain_item = "hush-discord"
application_id = "345678901234567890"

[network]
require_tailscale = true
allowed_cidrs = ["100.64.0.0/10", "127.0.0.0/8"]

[security]
require_file_mode_checks = false
require_keychain_acl = false
require_ntp_sync = false
`, v.Dir(), v.AuditPath(), v.RegistryPath())

	cfgPath := v.Dir() + "/server.toml"
	if err := os.WriteFile(cfgPath, []byte(tomlBody), 0o600); err != nil {
		t.Fatalf("harness.newTestServerConfig: write toml: %v", err)
	}
	cfg, err := config.LoadServer(t.Context(), cfgPath)
	if err != nil {
		t.Fatalf("harness.newTestServerConfig: LoadServer: %v", err)
	}
	return cfg
}
