package server

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/mrz1836/hush/internal/config"
	"github.com/mrz1836/hush/internal/token"
	"github.com/mrz1836/hush/internal/vault"
	"github.com/mrz1836/hush/internal/vault/securebytes"
)

// errFakeNoScripted is the canned error returned by [fakeApprover] when no
// scripted decision remains. Test-only sentinel — kept here so the err113
// linter is satisfied.
var errFakeNoScripted = errors.New("test: fakeApprover: no scripted decision")

// errTestSynthetic is the generic error used by tests that need an arbitrary
// non-nil error to drive an error path.
var errTestSynthetic = errors.New("test: synthetic error")

// errClockProbeStub is the canned error used by the platform-specific clock
// sync probe tests when stubbing the exec helper to fail.
var errClockProbeStub = errors.New("test: clock probe stub")

// captureLogger returns a slog.Logger backed by a buffer the caller can read.
func captureLogger(_ *testing.T) (*slog.Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	h := slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	return slog.New(h), buf
}

// recordingAudit captures every AuditEvent for assertion.
type recordingAudit struct {
	mu     sync.Mutex
	events []AuditEvent
	err    error
}

func (a *recordingAudit) Write(_ context.Context, e AuditEvent) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, e)
	return a.err
}

func (a *recordingAudit) snapshot() []AuditEvent {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]AuditEvent, len(a.events))
	copy(out, a.events)
	return out
}

// fakeApprover satisfies the Approver interface and records every call.
type fakeApprover struct {
	mu        sync.Mutex
	calls     []ApprovalRequest
	decisions []Decision
	errs      []error
	idx       int
}

func (f *fakeApprover) RequestApproval(_ context.Context, req ApprovalRequest) (Decision, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, req)
	if f.idx >= len(f.decisions) {
		return Decision{}, errFakeNoScripted
	}
	d, e := f.decisions[f.idx], f.errs[f.idx]
	f.idx++
	return d, e
}

// fakeStore implements vault.Store. Each Get returns a fresh SecureBytes
// holding the configured payload bytes; Destroy increments destroyCount.
type fakeStore struct {
	mu           sync.Mutex
	payload      []byte
	destroyCount int
	destroyErr   error
	tag          string
}

func newFakeStore(tag string, payload []byte) *fakeStore {
	return &fakeStore{tag: tag, payload: append([]byte(nil), payload...)}
}

func (f *fakeStore) Get(_ string) (*securebytes.SecureBytes, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return securebytes.New(append([]byte(nil), f.payload...))
}

func (f *fakeStore) Names() []string {
	return []string{f.tag}
}

func (f *fakeStore) Destroy() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.destroyCount++
	return f.destroyErr
}

func (f *fakeStore) destroys() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.destroyCount
}

// stubInterfaceLister returns a list with a single 100.64.1.1/10 address —
// satisfies the tailscale_bind check for unit tests.
func stubInterfaceLister(addr netip.Addr) func() ([]net.Addr, error) {
	return func() ([]net.Addr, error) {
		_, ipNet, _ := net.ParseCIDR(addr.String() + "/32")
		return []net.Addr{ipNet}, nil
	}
}

// rwxStateDir creates a fresh state directory under t.TempDir() with mode
// 0700 (matching the file_modes contract) and returns its path.
func rwxStateDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // 0700 is the chassis-required state-dir mode
		t.Fatalf("chmod state dir: %v", err)
	}
	return dir
}

// chmod0644File creates a file under stateDir with mode 0644. Used to drive
// the file_modes laxer-mode test.
func chmod0644File(t *testing.T, stateDir, name string) {
	t.Helper()
	p := filepath.Join(stateDir, name)
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if err := os.Chmod(p, 0o644); err != nil { //nolint:gosec // intentional 0644 to drive negative test
		t.Fatalf("chmod 0644: %v", err)
	}
}

// writeMode sets the mode of path to perm. Helper for negative tests.
func writeMode(t *testing.T, path string, perm os.FileMode) {
	t.Helper()
	if err := os.Chmod(path, perm); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}
}

// testCfg returns a minimal *config.Server suitable for unit tests. The
// caller may override fields after construction.
func testCfg(t *testing.T) *config.Server {
	t.Helper()
	stateDir := rwxStateDir(t)
	listenAP := netip.MustParseAddrPort("100.64.1.1:7743")
	c := &config.Server{
		Server: config.ServerSection{
			ListenAddr: listenAP,
			PathPrefix: "abcdef",
			StateDir:   stateDir,
			AuditLog:   filepath.Join(stateDir, "audit.jsonl"),
		},
		Network: config.NetworkSection{
			RequireTailscale: true,
			AllowedCIDRs:     []string{"100.64.0.0/10"},
			HealthBind:       listenAP,
		},
		Security: config.SecuritySection{
			RequireFileModeChecks: true,
			RequireKeychainACL:    false,
			RequireNTPSync:        true,
			MaxClockDrift:         60 * time.Second,
		},
	}
	return c
}

// alwaysSyncedClockProbe returns synced=true / drift=0 / err=nil.
func alwaysSyncedClockProbe(_ context.Context) (bool, time.Duration, error) {
	return true, 0, nil
}

// scriptedClockProbe returns whatever the caller passes.
func scriptedClockProbe(synced bool, drift time.Duration, err error) func(context.Context) (bool, time.Duration, error) {
	return func(_ context.Context) (bool, time.Duration, error) {
		return synced, drift, err
	}
}

// newTestServer constructs a Server with a populated initial vault store.
// Returns the constructed server, the inspectable audit recorder, the
// inspectable fake approver, and a buffer that captures the logger's output.
func newTestServer(t *testing.T, mods ...func(d *Deps)) (*Server, *recordingAudit, *fakeApprover, *bytes.Buffer) { //nolint:unparam // approver+buf returned for tests that need to inspect them
	t.Helper()
	logger, buf := captureLogger(t)
	audit := &recordingAudit{}
	approver := &fakeApprover{}

	cfg := testCfg(t)

	initial := vault.Store(newFakeStore("A", []byte("vault-a")))
	var ptr atomic.Pointer[vault.Store]
	ptr.Store(&initial)

	deps := Deps{
		Cfg:             cfg,
		VaultPtr:        &ptr,
		TokenStore:      token.NewStore(),
		Approver:        approver,
		Logger:          logger,
		AuditWriter:     audit,
		Clock:           func() time.Time { return time.Unix(0, 0) },
		ClockSyncProbe:  alwaysSyncedClockProbe,
		InterfaceLister: stubInterfaceLister(cfg.Server.ListenAddr.Addr()),
	}
	for _, m := range mods {
		m(&deps)
	}

	srv, err := New(deps)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return srv, audit, approver, buf
}

// canTestForeignOwner reports whether the current process can run a "owned
// by another user" test. It returns false on hosts where no second uid is
// available (most CI containers).
func canTestForeignOwner() bool {
	if syscall.Getuid() != 0 {
		// Non-root cannot chown to a different uid; the negative test is
		// then "stat returns Uid != Getuid()", which we cannot fabricate
		// without root.
		return false
	}
	return true
}

// stubLoadVault returns a LoadVaultFn that yields the supplied store/error
// on each call.
func stubLoadVault(store vault.Store, err error) func(ctx context.Context, path string, key *securebytes.SecureBytes) (vault.Store, error) {
	return func(_ context.Context, _ string, _ *securebytes.SecureBytes) (vault.Store, error) {
		return store, err
	}
}

// makeKey builds a tiny *securebytes.SecureBytes for tests that need a
// non-nil vault key.
func makeKey(t *testing.T) *securebytes.SecureBytes {
	t.Helper()
	k, err := securebytes.New([]byte("test-vault-key-32-bytes--padding"))
	if err != nil {
		t.Fatalf("securebytes.New: %v", err)
	}
	return k
}

// asPrefix parses s into a [netip.Prefix]; t.Fatal on failure.
func asPrefix(t *testing.T, s string) netip.Prefix {
	t.Helper()
	p, err := netip.ParsePrefix(s)
	if err != nil {
		t.Fatalf("ParsePrefix %q: %v", s, err)
	}
	return p
}

// makeSymlink wraps os.Symlink for tests.
func makeSymlink(target, link string) error { return os.Symlink(target, link) }

// writeFile wraps os.WriteFile.
func writeFile(p string, data []byte, perm os.FileMode) error {
	return os.WriteFile(p, data, perm)
}

// makeDir wraps os.Mkdir.
func makeDir(p string, perm os.FileMode) error { return os.Mkdir(p, perm) }

// chmod wraps os.Chmod.
func chmod(p string, perm os.FileMode) error { return os.Chmod(p, perm) }

// _okHandler returns a 200-OK handler used by middleware coverage tests.
func _okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

// _panickingHandler returns a handler that panics with v.
func _panickingHandler(v any) http.Handler {
	return http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		panic(v)
	})
}

// makeReq returns an httptest request with RemoteAddr=remote.
func makeReq(t *testing.T, remote string) *http.Request {
	t.Helper()
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	r.RemoteAddr = remote
	return r
}

// makeRec returns a fresh httptest.ResponseRecorder.
func makeRec() *httptest.ResponseRecorder { return httptest.NewRecorder() }
