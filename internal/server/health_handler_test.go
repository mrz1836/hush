package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mrz1836/hush/internal/testutil"
	"github.com/mrz1836/hush/internal/token"
	"github.com/mrz1836/hush/internal/vault"
)

type healthTestHarness struct {
	t          *testing.T
	server     *Server
	audit      *recordingAudit
	tokenStore token.Store
	store      *secretFakeStore
}

func newHealthHarness(t *testing.T, vaultValues map[string][]byte, mods ...func(*Deps)) *healthTestHarness {
	t.Helper()
	cfg := testCfg(t)
	logger, _ := captureClaimLogger(t)
	auditRec := &recordingAudit{}
	tokenStore := token.NewStore()
	store := &secretFakeStore{values: vaultValues}
	initial := vault.Store(store)
	var ptr atomic.Pointer[vault.Store]
	ptr.Store(&initial)

	deps := Deps{
		Cfg:             cfg,
		VaultPtr:        &ptr,
		TokenStore:      tokenStore,
		TokenIssuer:     noopTokenIssuer,
		Approver:        &fakeApprover{},
		Logger:          logger,
		AuditWriter:     auditRec,
		Clock:           time.Now,
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
	srv.runStartedAt = time.Now().Add(-90 * time.Second)
	srv.clockInSync.Store(true)
	return &healthTestHarness{
		t:          t,
		server:     srv,
		audit:      auditRec,
		tokenStore: tokenStore,
		store:      store,
	}
}

func (h *healthTestHarness) do(t *testing.T) (*httptest.ResponseRecorder, healthResponse) {
	t.Helper()
	chassisID := freshChassisID()
	ctx := context.WithValue(t.Context(), requestIDKey, chassisID)
	r := httptest.NewRequestWithContext(ctx, http.MethodGet, "/h/abcdef/hz", nil)
	r.RemoteAddr = "100.64.1.5:43210"
	rr := httptest.NewRecorder()
	h.server.handleHealth(rr, r)

	var resp healthResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v (body=%s)", err, rr.Body.String())
	}
	return rr, resp
}

func TestHealth_NoAuth_OK(t *testing.T) {
	t.Parallel()
	h := newHealthHarness(t, map[string][]byte{"A": []byte("v"), "B": []byte("w")})
	rr, resp := h.do(t)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", rr.Code)
	}
	if resp.Status != "ok" {
		t.Fatalf("status=%q", resp.Status)
	}
	if resp.Uptime == "" {
		t.Fatal("uptime empty")
	}
	if !resp.VaultLoaded {
		t.Fatal("vault_loaded=false; want true")
	}
	if !resp.ConfigValid {
		t.Fatal("config_valid=false")
	}
	if !resp.ClockInSync {
		t.Fatal("clock_in_sync=false")
	}
	if resp.SecretsCount != 2 {
		t.Fatalf("secrets_count=%d want 2", resp.SecretsCount)
	}
	if rr.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("content-type=%q", rr.Header().Get("Content-Type"))
	}
	// No audit event recorded.
	if got := len(h.audit.snapshot()); got != 0 {
		t.Fatalf("/hz emitted %d audit events; want 0", got)
	}
}

func TestHealth_DiscordConnectedFlag(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		fn   func() bool
		want bool
	}{
		{"nil", nil, false},
		{"true", func() bool { return true }, true},
		{"false", func() bool { return false }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newHealthHarness(t, map[string][]byte{"X": []byte("v")}, func(d *Deps) {
				d.DiscordHealth = tc.fn
			})
			_, resp := h.do(t)
			if resp.DiscordConnected != tc.want {
				t.Fatalf("discord_connected=%v want %v", resp.DiscordConnected, tc.want)
			}
		})
	}
}

func TestHealth_VaultLoadedFalseDuringStartup(t *testing.T) {
	t.Parallel()
	h := newHealthHarness(t, map[string][]byte{"X": []byte("v")})
	var nilPtr atomic.Pointer[vault.Store]
	h.server.vaultPtr = &nilPtr
	rr, resp := h.do(t)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", rr.Code)
	}
	if resp.VaultLoaded {
		t.Fatal("vault_loaded=true; want false")
	}
	if resp.SecretsCount != 0 {
		t.Fatalf("secrets_count=%d want 0", resp.SecretsCount)
	}
}

func TestHealth_NoSentinelLeak(t *testing.T) {
	t.Parallel()
	sentinel := testutil.SentinelSecret(13)
	// Plant the sentinel in a vault NAME and the bot-token-equivalent
	// (DiscordHealth closure won't surface anything; this primarily
	// verifies vault names don't escape to the body).
	h := newHealthHarness(t, map[string][]byte{
		"S_" + sentinel: []byte("v"),
	})
	rr, resp := h.do(t)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", rr.Code)
	}
	if resp.SecretsCount != 1 {
		t.Fatalf("secrets_count=%d want 1", resp.SecretsCount)
	}
	testutil.AssertSentinelAbsent(t, sentinel, rr.Body.String())
}

func TestHealth_ActiveTokensCount(t *testing.T) {
	t.Parallel()
	h := newHealthHarness(t, map[string][]byte{"X": []byte("v")})
	for i := 0; i < 3; i++ {
		tok := &token.Token{
			JTI:         "jti-" + string(rune('a'+i)),
			Encoded:     "enc",
			ExpiresAt:   time.Now().Add(time.Hour),
			SessionType: token.SessionInteractive,
			MaxUses:     10,
		}
		if err := h.tokenStore.Add(tok); err != nil {
			t.Fatalf("Add: %v", err)
		}
	}
	if err := h.tokenStore.Revoke("jti-b"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	_, resp := h.do(t)
	if resp.ActiveTokens != 2 {
		t.Fatalf("active_tokens=%d want 2 (revoked excluded)", resp.ActiveTokens)
	}
}

func TestHealth_ZeroUptimeBeforeRun(t *testing.T) {
	t.Parallel()
	h := newHealthHarness(t, map[string][]byte{"X": []byte("v")})
	h.server.runStartedAt = time.Time{}
	_, resp := h.do(t)
	if resp.Uptime != "0s" {
		t.Fatalf("uptime=%q want 0s", resp.Uptime)
	}
}

// TestHealth_BackwardClockJumpClampedToZero asserts that a wall-clock
// jump backward (NTP step, VM live-migrate) does not surface a negative
// uptime to /hz clients.
func TestHealth_BackwardClockJumpClampedToZero(t *testing.T) {
	t.Parallel()
	h := newHealthHarness(t, map[string][]byte{"X": []byte("v")})
	// Pin runStartedAt to a wall-clock instant in the future, simulating
	// the system clock having stepped backward since the chassis booted.
	h.server.runStartedAt = time.Now().Add(5 * time.Minute)
	_, resp := h.do(t)
	if resp.Uptime != "0s" {
		t.Fatalf("uptime=%q want 0s (clamped from negative)", resp.Uptime)
	}
	if strings.HasPrefix(resp.Uptime, "-") {
		t.Fatalf("uptime=%q has negative prefix", resp.Uptime)
	}
}
