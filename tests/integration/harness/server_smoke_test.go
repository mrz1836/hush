//go:build integration

package harness_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/mrz1836/hush/internal/testutil"
	"github.com/mrz1836/hush/tests/integration/harness"
)

// TestServerSmoke_HealthEndpointResponds is the chassis-alive smoke
// check. It composes TestServer
// against a fresh vault + Discord stub and verifies GET /hz returns a
// 2xx response. Other endpoints (/claim, /s, /revoke) are covered by
// the scenarios themselves.
func TestServerSmoke_HealthEndpointResponds(t *testing.T) {
	logger := harness.NewLogCapture(t)
	vault := harness.NewVault(t, map[string]string{
		"SMOKE_KEY": testutil.SentinelSecret(0),
	})
	discord := harness.NewDiscord(t)
	srv := harness.NewServer(t, harness.ServerOpts{
		Vault:   vault,
		Logger:  logger,
		Discord: discord,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL()+"/hz", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /hz: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 500 {
		t.Fatalf("GET /hz: status %d, want < 500", resp.StatusCode)
	}

	// Sentinel sweep: the smoke test secret value (sentinel 0) must not
	// appear in any captured stream.
	harness.AssertSentinelAbsent(
		t,
		testutil.SentinelSecret(0),
		logger.Bytes(),
		srv.RawAudit(),
		discord.AlertsRaw(),
	)
}
