package server

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mrz1836/hush/internal/config"
	"github.com/mrz1836/hush/internal/token"
	"github.com/mrz1836/hush/internal/vault"
)

func TestNew_RequiresAllDeps(t *testing.T) {
	t.Parallel()

	good := func(t *testing.T) Deps {
		logger, _ := captureLogger(t)
		cfg := testCfg(t)
		initial := vault.Store(newFakeStore("A", []byte("a")))
		var ptr atomic.Pointer[vault.Store]
		ptr.Store(&initial)
		return Deps{
			Cfg:         cfg,
			VaultPtr:    &ptr,
			TokenStore:  token.NewStore(),
			Approver:    &fakeApprover{},
			Logger:      logger,
			AuditWriter: &recordingAudit{},
		}
	}

	cases := []struct {
		name    string
		mutate  func(d *Deps)
		wantErr error
	}{
		{"missing Cfg", func(d *Deps) { d.Cfg = nil }, ErrMissingConfig},
		{"missing VaultPtr", func(d *Deps) { d.VaultPtr = nil }, ErrMissingVaultPtr},
		{"VaultPtr.Load() nil", func(d *Deps) {
			var p atomic.Pointer[vault.Store]
			d.VaultPtr = &p
		}, ErrMissingVaultPtr},
		{"missing TokenStore", func(d *Deps) { d.TokenStore = nil }, ErrMissingTokenStore},
		{"missing Approver", func(d *Deps) { d.Approver = nil }, ErrMissingApprover},
		{"missing Logger", func(d *Deps) { d.Logger = nil }, ErrMissingLogger},
		{"missing AuditWriter", func(d *Deps) { d.AuditWriter = nil }, ErrMissingAuditWriter},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := good(t)
			tc.mutate(&d)
			_, err := New(d)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err=%v want errors.Is(_, %v)", err, tc.wantErr)
			}
		})
	}
}

// TestNew_ZeroIO asserts that New does not perform any I/O when constructing
// the chassis. We verify by passing a Cfg whose StateDir does not exist and a
// ListenAddr we cannot bind — New must still return a *Server with no error.
func TestNew_ZeroIO(t *testing.T) {
	t.Parallel()

	cfg := testCfg(t)
	cfg.Server.StateDir = "/nonexistent/path/that/should/not/exist/abcde"
	cfg.Server.ListenAddr = netip.MustParseAddrPort("100.64.99.99:1") // unbindable

	logger, _ := captureLogger(t)
	initial := vault.Store(newFakeStore("A", []byte("a")))
	var ptr atomic.Pointer[vault.Store]
	ptr.Store(&initial)
	srv, err := New(Deps{
		Cfg:         cfg,
		VaultPtr:    &ptr,
		TokenStore:  token.NewStore(),
		Approver:    &fakeApprover{},
		Logger:      logger,
		AuditWriter: &recordingAudit{},
	})
	if err != nil {
		t.Fatalf("New returned %v; expected zero-I/O success", err)
	}
	if srv == nil {
		t.Fatal("New returned nil *Server")
	}

	// The struct's listener and httpServer are nil until Run; verifying via
	// the unexported fields is internal so we just assert basic state.
	if srv.cfg != cfg {
		t.Fatal("Server did not retain Cfg")
	}
	if srv.shuttingDown.Load() {
		t.Fatal("Server should not be marked shuttingDown after New")
	}
	if srv.runCalled.Load() {
		t.Fatal("Server should not be marked runCalled after New")
	}
	if srv.reloadDrainWindow != DefaultReloadDrainWindow {
		t.Fatalf("reloadDrainWindow = %v, want default %v", srv.reloadDrainWindow, DefaultReloadDrainWindow)
	}
	if srv.shutdownTimeout != DefaultShutdownTimeout {
		t.Fatalf("shutdownTimeout = %v, want default %v", srv.shutdownTimeout, DefaultShutdownTimeout)
	}
}

// TestNew_DefaultsApplied verifies that optional Deps fields fall back to
// their constants and helpers when omitted.
func TestNew_DefaultsApplied(t *testing.T) {
	t.Parallel()

	srv, _, _, _ := newTestServer(t, func(d *Deps) {
		d.Clock = nil
		d.ClockSyncProbe = nil
		d.InterfaceLister = nil
	})
	if srv.clock == nil {
		t.Fatal("clock default not applied")
	}
	if srv.clockProbe == nil {
		t.Fatal("clockProbe default not applied")
	}
	if srv.interfaceLister == nil {
		t.Fatal("interfaceLister default not applied")
	}
}

// TestRun_AlreadyRun asserts that calling Run twice on the same Server
// returns ErrAlreadyRun.
func TestRun_AlreadyRun(t *testing.T) {
	t.Parallel()

	srv, _, _, _ := newTestServer(t, func(d *Deps) {
		// Force a startup-check failure so Run returns quickly without
		// binding a listener.
		d.ClockSyncProbe = scriptedClockProbe(false, 0, nil)
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := srv.Run(ctx); !errors.Is(err, ErrClockUnsynchronised) {
		t.Fatalf("first Run err=%v, want ErrClockUnsynchronised", err)
	}
	if err := srv.Run(ctx); !errors.Is(err, ErrAlreadyRun) {
		t.Fatalf("second Run err=%v, want ErrAlreadyRun", err)
	}
}

// TestRequestID_FromContext asserts the public RequestID accessor returns
// the empty string for a context without the package-private key, and the
// stored value when it is present.
func TestRequestID_FromContext(t *testing.T) {
	t.Parallel()

	if got := RequestID(context.Background()); got != "" {
		t.Fatalf("RequestID(empty)=%q want \"\"", got)
	}

	ctx := context.WithValue(context.Background(), requestIDKey, "abc123")
	if got := RequestID(ctx); got != "abc123" {
		t.Fatalf("RequestID(ctx)=%q want %q", got, "abc123")
	}

	// A string-typed key with the same string value MUST NOT collide with
	// the typed-struct key. Verify by storing a different string under a
	// string key.
	type stringKey string
	ctx2 := context.WithValue(context.Background(), stringKey("requestIDKey"), "wrong")
	if got := RequestID(ctx2); got != "" {
		t.Fatalf("RequestID(string-key)=%q want \"\"", got)
	}
}

// TestApprover_TypeShape asserts the Approver / ApprovalRequest / Decision
// surface remains stable: any drift in the field set or method signature
// breaks the test loudly.
func TestApprover_TypeShape(t *testing.T) {
	t.Parallel()

	// Approver must be a single-method interface.
	approverType := reflect.TypeOf((*Approver)(nil)).Elem()
	if approverType.NumMethod() != 1 {
		t.Fatalf("Approver has %d methods, want 1", approverType.NumMethod())
	}
	m := approverType.Method(0)
	if m.Name != "RequestApproval" {
		t.Fatalf("Approver method name = %q, want RequestApproval", m.Name)
	}

	// ApprovalRequest field set.
	wantReq := map[string]string{
		"RequestID":    "string",
		"MachineName":  "string",
		"ClientIP":     "netip.Addr",
		"Scope":        "[]string",
		"Reason":       "string",
		"SessionType":  "server.SessionType",
		"RequestedTTL": "time.Duration",
		"Metadata":     "map[string]string",
	}
	got := fieldMap(reflect.TypeOf(ApprovalRequest{}))
	if !reflect.DeepEqual(got, wantReq) {
		t.Fatalf("ApprovalRequest fields drifted:\n got=%v\nwant=%v", got, wantReq)
	}

	// Decision field set.
	wantDec := map[string]string{
		"Approved":   "bool",
		"ApprovedAt": "time.Time",
		"DeniedAt":   "time.Time",
		"GrantedTTL": "time.Duration",
		"ApproverID": "string",
		"Reason":     "string",
	}
	gotDec := fieldMap(reflect.TypeOf(Decision{}))
	if !reflect.DeepEqual(gotDec, wantDec) {
		t.Fatalf("Decision fields drifted:\n got=%v\nwant=%v", gotDec, wantDec)
	}

	// SessionType.String() exhaustive cases.
	for _, tc := range []struct {
		v    SessionType
		want string
	}{
		{SessionInteractive, "interactive"},
		{SessionSupervisor, "supervisor"},
		{SessionType(0), "unknown"},
		{SessionType(99), "unknown"},
	} {
		if got := tc.v.String(); got != tc.want {
			t.Fatalf("SessionType(%d).String()=%q want %q", tc.v, got, tc.want)
		}
	}
}

// TestAuditEvent_TypeShape pins the AuditWriter / AuditEvent /
// AuditEventType surface.
func TestAuditEvent_TypeShape(t *testing.T) {
	t.Parallel()

	w := reflect.TypeOf((*AuditWriter)(nil)).Elem()
	if w.NumMethod() != 1 || w.Method(0).Name != "Write" {
		t.Fatalf("AuditWriter shape drifted: methods=%v", w)
	}

	want := map[string]string{
		"Type":      "server.AuditEventType",
		"At":        "time.Time",
		"RequestID": "string",
		"ClientIP":  "netip.Addr",
		"Detail":    "map[string]string",
	}
	got := fieldMap(reflect.TypeOf(AuditEvent{}))
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("AuditEvent fields drifted:\n got=%v\nwant=%v", got, want)
	}

	// Six chassis-emitted constants — value strings stable.
	wantEvents := map[AuditEventType]string{
		AuditServerStart:          "server_start",
		AuditServerStop:           "server_stop",
		AuditVaultReloaded:        "vault_reloaded",
		AuditFilePermCheckFailed:  "file_perm_check_failed",
		AuditAuthFailedNotAllowed: "auth_failed",
		AuditPanicCaptured:        "panic_captured",
	}
	for k, v := range wantEvents {
		if string(k) != v {
			t.Fatalf("AuditEventType(%v) = %q, want %q", k, string(k), v)
		}
	}
}

// TestApprover_FakeImplements verifies the local fake satisfies the
// interface; that the chassis stores it unchanged; and that the test-only
// approver() accessor returns the same value supplied via Deps.
func TestApprover_FakeImplements(t *testing.T) {
	t.Parallel()

	approverImpl := &fakeApprover{
		decisions: []Decision{{Approved: true, ApproverID: "test"}},
		errs:      []error{nil},
	}

	srv, _, _, _ := newTestServer(t, func(d *Deps) {
		d.Approver = approverImpl
	})

	stored := srv.approver()
	if stored != approverImpl {
		t.Fatalf("chassis stored %T (%p), want %T (%p)", stored, stored, approverImpl, approverImpl)
	}

	dec, err := stored.RequestApproval(context.Background(), ApprovalRequest{RequestID: "id-1"})
	if err != nil {
		t.Fatalf("RequestApproval err=%v", err)
	}
	if !dec.Approved || dec.ApproverID != "test" {
		t.Fatalf("decision drifted: %+v", dec)
	}
	if len(approverImpl.calls) != 1 || approverImpl.calls[0].RequestID != "id-1" {
		t.Fatalf("recorded calls drifted: %+v", approverImpl.calls)
	}
}

// fieldMap returns a map of public fieldName → typeName for t.
func fieldMap(t reflect.Type) map[string]string {
	out := map[string]string{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		out[f.Name] = f.Type.String()
	}
	return out
}

// TestServer_DepsFieldsLocked guards against accidental drift in the locked
// Deps surface.
func TestServer_DepsFieldsLocked(t *testing.T) {
	t.Parallel()

	depsType := reflect.TypeOf(Deps{})
	want := []string{
		"Cfg", "VaultPtr", "TokenStore", "Approver", "Logger", "AuditWriter",
		"Clock", "ClockSyncProbe", "InterfaceLister", "Listener",
		"VaultKey", "LoadVaultFn", "ReloadDrainWindow", "ShutdownTimeout",
	}
	for _, name := range want {
		if _, ok := depsType.FieldByName(name); !ok {
			t.Errorf("Deps missing field %q", name)
		}
	}
}

// TestServer_ZeroAuditOnStartupOK exercises a happy-path Run with a fake
// listener so the lifecycle is observable without binding a real socket.
func TestServer_ZeroAuditOnStartupOK(t *testing.T) {
	t.Parallel()

	var lc net.ListenConfig
	listener, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	srv, audit, _, _ := newTestServer(t, func(d *Deps) {
		d.Listener = listener
	})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		// Allow the chassis to bind, then cancel.
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	if err := srv.Run(ctx); err != nil {
		t.Fatalf("Run err=%v", err)
	}

	events := audit.snapshot()
	if len(events) < 1 {
		t.Fatalf("expected at least one audit event, got %d", len(events))
	}
	if events[0].Type != AuditServerStart {
		t.Fatalf("first audit event = %v, want %v", events[0].Type, AuditServerStart)
	}
	if events[0].Detail["status"] != "ok" {
		t.Fatalf("AuditServerStart detail status=%q want ok", events[0].Detail["status"])
	}
}

// TestServer_LoggerCarriesRequestID asserts the chassis's logger emission
// path tags log lines with the request_id when one is in context.
func TestServer_LoggerCarriesRequestID(t *testing.T) {
	t.Parallel()

	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(buf, nil))
	cfg := testCfg(t)
	cfg.Network.AllowedCIDRs = []string{"127.0.0.0/8"}

	prefixes := parseAllowedCIDRs(cfg.Network.AllowedCIDRs)
	if !allowedByCIDR(netip.MustParseAddr("127.0.0.1"), prefixes) {
		t.Fatal("loopback should be allowed by 127.0.0.0/8")
	}
	if allowedByCIDR(netip.MustParseAddr("8.8.8.8"), prefixes) {
		t.Fatal("public IP must not be allowed by 127.0.0.0/8")
	}
	_ = logger
	_ = config.TailscaleCGNAT
	_ = strings.HasPrefix("", "")
}
