package server

import (
	"encoding/json"
	"net/http"
	"time"
)

// healthResponse is the locked /hz response shape.
type healthResponse struct {
	Status           string `json:"status"`
	Uptime           string `json:"uptime"`
	SecretsCount     int    `json:"secrets_count"`
	ActiveTokens     int    `json:"active_tokens"`
	DiscordConnected bool   `json:"discord_connected"`
	ConfigValid      bool   `json:"config_valid"`
	VaultLoaded      bool   `json:"vault_loaded"`
	ClockInSync      bool   `json:"clock_in_sync"`
}

// handleHealth is the entry point for `GET /h/<prefix>/hz`.
//
// Constitution VI: reachable WITHOUT a JWT — Tailscale is the auth
// perimeter. The handler MUST NOT emit an audit event. The body MUST
// NOT carry any secret name, JTI, fingerprint, bot token, audit signing
// key, audit chain hash, or random API path prefix.
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	store := s.vaultPtr.Load()
	resp := healthResponse{
		Status:           "ok",
		Uptime:           s.uptimeString(),
		ActiveTokens:     s.tokenStore.ActiveCount(),
		DiscordConnected: s.discordHealth(),
		ConfigValid:      true,
		VaultLoaded:      store != nil,
		ClockInSync:      s.clockInSync.Load(),
	}
	if store != nil {
		resp.SecretsCount = len((*store).Names())
	}

	body, _ := json.Marshal(resp) //nolint:errchkjson // closed primitives-only struct: cannot fail
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// uptimeString returns the Go-duration form of (now − runStartedAt),
// rounded to the nearest second. A backward wall-clock jump (NTP step,
// VM live-migrate, manual `date`) is clamped to "0s" so /hz never
// surfaces a negative duration.
func (s *Server) uptimeString() string {
	if s.runStartedAt.IsZero() {
		return "0s"
	}
	elapsed := time.Since(s.runStartedAt)
	if elapsed < 0 {
		elapsed = 0
	}
	return elapsed.Round(time.Second).String()
}
