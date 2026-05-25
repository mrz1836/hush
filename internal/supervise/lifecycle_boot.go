// Supervisor orchestration glue: boot path.
//
// lifecycle_boot.go owns the boot precondition loop (Tailscale + vault /hz)
// with exponential backoff jittered ±20% capped at 30s/attempt, the signed
// /claim submission, the JWT persistence into Store via the package-private
// setToken seam, the initial Refiller.Refill, the validator pass, and child
// env construction + first Child.Start.

package supervise

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/mrz1836/hush/internal/transport/sign"
	"github.com/mrz1836/hush/internal/vault/securebytes"
)

// Package-private boot-path sentinels for err113 compliance.
var (
	errClaimEmptyJWT = errors.New("supervise: empty JWT")
	errHzNon200      = errors.New("supervise: /hz non-200")
	errEnvBuildScope = errors.New("supervise: env build scope add failed")
)

// claimWireRequest mirrors internal/server/claim_handler.go::claimRequest.
// SupervisorName is populated from the supervisor's [name] config field
// and is required by the server when session_type=supervisor.
type claimWireRequest struct {
	Scope                []string `json:"scope"`
	Reason               string   `json:"reason"`
	TTL                  string   `json:"ttl"`
	SessionType          string   `json:"session_type"`
	EphemeralPubKey      string   `json:"ephemeral_pubkey"`
	Nonce                string   `json:"nonce"`
	Timestamp            string   `json:"timestamp"`
	Signature            string   `json:"signature"`
	RequestID            string   `json:"request_id"`
	MachineName          string   `json:"machine_name"`
	SupervisorName       string   `json:"supervisor_name,omitempty"`
	ClientKeyFingerprint string   `json:"client_key_fingerprint"`
}

// claimSignedPayload mirrors internal/server/claim_handler.go::signedPayload.
// Agent-context fields (PR 4) are unused by the supervisor today but
// must be present in the struct for canonical-JSON parity with the
// server — CanonicalJSON ignores omitempty and includes every exported
// field, so adding the fields here makes the supervisor's signature
// byte-identical to the server's expectation.
type claimSignedPayload struct {
	AgentIdentity   string   `json:"agent_identity,omitempty"`
	AgentModel      string   `json:"agent_model,omitempty"`
	CommandPreview  string   `json:"command_preview,omitempty"`
	EphemeralPubKey string   `json:"ephemeral_pubkey"`
	MachineName     string   `json:"machine_name"`
	Nonce           string   `json:"nonce"`
	Reason          string   `json:"reason"`
	RecentSummary   string   `json:"recent_summary,omitempty"`
	RequestID       string   `json:"request_id"`
	Scope           []string `json:"scope"`
	SessionType     string   `json:"session_type"`
	SupervisorName  string   `json:"supervisor_name,omitempty"`
	Timestamp       string   `json:"timestamp"`
	ToolName        string   `json:"tool_name,omitempty"`
	TTL             string   `json:"ttl"`
}

// claimWireResponse decodes the server's success body.
type claimWireResponse struct {
	JWT       string `json:"jwt"`
	ExpiresAt string `json:"expires_at"`
	JTI       string `json:"jti"`
}

// claimWireError decodes the server's failure body.
type claimWireError struct {
	Error     string `json:"error"`
	RequestID string `json:"request_id"`
}

// boot drives the full boot path: probes → /claim → initial refill →
// validators → first child start. Returns nil on entry into mainLoop with
// the child running; returns a wrapped terminal error on permanent failure.
func (l *Lifecycle) boot(ctx context.Context) error {
	if err := l.bootPreconditionsLoop(ctx); err != nil {
		return err
	}
	if err := l.submitClaim(ctx); err != nil {
		return err
	}
	return l.initialRefillAndStart(ctx)
}

// bootPreconditionsLoop is the exponential-backoff loop against Tailscale +
// vault /hz. On exhaustion: emit AlertClassBootTimeout, append
// ActionSupervisorBootTimeout, return wrapped ErrBootTimeout.
//
//nolint:gocognit,gocyclo // sequential probe+backoff loop with explicit budget tracking
func (l *Lifecycle) bootPreconditionsLoop(ctx context.Context) error {
	budget := l.config.BootRetryTimeout
	if budget <= 0 {
		budget = 10 * time.Minute
	}
	deadline := l.deps.NowFn().Add(budget)
	backoff := bootBackoffInitial
	var lastErrClass string

	for attempt := 0; ; attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		probeCtx, cancel := context.WithTimeout(ctx, bootProbeTimeout)
		tsErr := l.deps.TailscaleProbe(probeCtx)
		var vaultErr error
		if tsErr == nil {
			vaultErr = l.deps.VaultHzProbe(probeCtx, l.config.ServerURL)
		}
		cancel()

		if tsErr == nil && vaultErr == nil {
			return nil
		}
		switch {
		case tsErr != nil:
			lastErrClass = "tailscale_unreachable"
		case vaultErr != nil:
			lastErrClass = "vault_unreachable"
		}
		l.deps.Logger.Info(
			"supervise: boot precondition not ready",
			slog.Int("attempt", attempt),
			slog.String("class", lastErrClass),
			slog.Any("tailscale_err", tsErr),
			slog.Any("vault_err", vaultErr),
		)

		// Backoff with ±20% jitter.
		sleep := jitterInterval(backoff)
		next := l.deps.NowFn().Add(sleep)
		if next.After(deadline) {
			// Exhaustion.
			l.deps.Alerts.Emit(ctx, AlertClassBootTimeout, AlertPayload{
				ErrorClass: lastErrClass,
				Reason:     alertReasonFor(AlertClassBootTimeout),
			})
			l.emitBootTimeout(ctx, lastErrClass)
			// Drive the documented state transition; the table maps
			// EventBootRetryExhausted → StateStopped (state.go:98).
			l.transition(ctx, EventBootRetryExhausted)
			return fmt.Errorf("supervise: boot: %w", ErrBootTimeout)
		}
		timer := time.NewTimer(sleep)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		// Compound the backoff with the multiplier and cap.
		next2 := time.Duration(float64(backoff) * bootBackoffMultiplier)
		if next2 > bootBackoffCap {
			next2 = bootBackoffCap
		}
		backoff = next2
	}
}

// submitClaim sends a signed /claim payload to <ServerURL>/claim, persists
// the JWT into Store via setToken, and emits ActionSupervisorSessionClaimed.
// On transient approval-path responses (Discord unavailable, approval timeout,
// rate limit, or 5xx), retries in-process with exponential backoff until the
// boot budget is exhausted. Keeping these retries inside one process is
// important under launchd: if we exit on 429, KeepAlive + ThrottleInterval turns
// the server-side rate limiter into a 10-second Discord audit spam loop.
// On 401 / 403 and other non-transient 4xx, returns a wrapped ErrClaimDenied
// terminal error.
//
//nolint:gocognit,gocyclo,cyclop // sequential build → sign → post → branch on response
func (l *Lifecycle) submitClaim(ctx context.Context) error {
	deadline := l.deps.NowFn().Add(l.config.BootRetryTimeout)
	backoff := bootBackoffInitial

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		resp, status, errBody, err := l.doClaimRequest(ctx)
		if err == nil && status == http.StatusOK {
			return l.applyClaimResponse(ctx, resp)
		}
		switch {
		case err != nil:
			l.deps.Logger.Warn("supervise: /claim transport error",
				slog.Any("err", err))
		case status == http.StatusUnauthorized:
			return fmt.Errorf("supervise: /claim 401: %w", ErrClaimDenied)
		case status == http.StatusForbidden:
			// Denied / bad_signature / etc are terminal.
			return fmt.Errorf("supervise: /claim 403 %q: %w", errBody.Error, ErrClaimDenied)
		case status == http.StatusRequestTimeout && errBody.Error == "approval_timeout":
			l.deps.Logger.Warn("supervise: /claim approval timed out; retrying within boot budget",
				slog.Int("status", status), slog.String("code", errBody.Error))
		case status == http.StatusTooManyRequests && errBody.Error == "rate_limited":
			l.deps.Logger.Warn("supervise: /claim rate limited; retrying within boot budget",
				slog.Int("status", status), slog.String("code", errBody.Error))
		case status == http.StatusServiceUnavailable && errBody.Error == "discord_unavailable":
			l.deps.Alerts.Emit(ctx, AlertClassDiscordUnavailableOnClaim, AlertPayload{
				ErrorClass: errorClassDiscordUnavailable,
				Reason:     alertReasonFor(AlertClassDiscordUnavailableOnClaim),
			})
		case status >= 500:
			l.deps.Logger.Warn("supervise: /claim 5xx; retrying",
				slog.Int("status", status), slog.String("code", errBody.Error))
		default:
			// Other 4xx — treat as terminal denied.
			return fmt.Errorf("supervise: /claim status=%d code=%q: %w", status, errBody.Error, ErrClaimDenied)
		}

		// Backoff before retry.
		sleep := jitterInterval(backoff)
		if l.deps.NowFn().Add(sleep).After(deadline) {
			l.deps.Alerts.Emit(ctx, AlertClassBootTimeout, AlertPayload{
				ErrorClass: errorClassDiscordUnavailable,
				Reason:     alertReasonFor(AlertClassBootTimeout),
			})
			l.emitBootTimeout(ctx, errorClassDiscordUnavailable)
			// Drive the documented terminal transition so the state machine
			// settles at StateStopped rather than freezing at StateFetching
			// when Run unwinds (mirrors bootPreconditionsLoop exhaustion).
			l.transition(ctx, EventBootRetryExhausted)
			return fmt.Errorf("supervise: boot: %w", ErrBootTimeout)
		}
		timer := time.NewTimer(sleep)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		next2 := time.Duration(float64(backoff) * bootBackoffMultiplier)
		if next2 > bootBackoffCap {
			next2 = bootBackoffCap
		}
		backoff = next2
	}
}

// doClaimRequest builds, signs, and POSTs the /claim. Returns the parsed
// success body OR (zero, status, errBody, nil) when status != 200 but the
// body was parseable; returns (zero, 0, zero, err) on transport / parse error.
func (l *Lifecycle) doClaimRequest(ctx context.Context) (claimWireResponse, int, claimWireError, error) {
	payload := l.buildClaimPayload()
	wire, signErr := signAndWrapClaim(ctx, l.deps.ClaimSigningKey, l.deps.ClientKeyFingerprint, payload)
	if signErr != nil {
		return claimWireResponse{}, 0, claimWireError{}, signErr
	}
	raw, mErr := json.Marshal(wire)
	if mErr != nil {
		return claimWireResponse{}, 0, claimWireError{}, fmt.Errorf("supervise: marshal claim: %w", mErr)
	}
	target := strings.TrimRight(l.config.ServerURL, "/") + "/claim"
	req, rErr := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(raw))
	if rErr != nil {
		return claimWireResponse{}, 0, claimWireError{}, fmt.Errorf("supervise: build claim request: %w", rErr)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, doErr := l.deps.HTTPClient.Do(req)
	if doErr != nil {
		return claimWireResponse{}, 0, claimWireError{}, fmt.Errorf("supervise: /claim: %w", doErr)
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))

	if resp.StatusCode == http.StatusOK {
		var ok claimWireResponse
		if jErr := json.Unmarshal(body, &ok); jErr != nil {
			return claimWireResponse{}, resp.StatusCode, claimWireError{}, fmt.Errorf("supervise: decode claim response: %w", jErr)
		}
		if ok.JWT == "" {
			return claimWireResponse{}, resp.StatusCode, claimWireError{}, errClaimEmptyJWT
		}
		return ok, resp.StatusCode, claimWireError{}, nil
	}
	var errBody claimWireError
	_ = json.Unmarshal(body, &errBody)
	return claimWireResponse{}, resp.StatusCode, errBody, nil
}

// applyClaimResponse wraps the JWT into a *SecureBytes, writes it into
// Store via setToken, parses the expiry, emits the audit event, updates
// statusInputs, and transitions the state machine.
func (l *Lifecycle) applyClaimResponse(ctx context.Context, resp claimWireResponse) error {
	sb, sbErr := securebytes.New([]byte(resp.JWT))
	if sbErr != nil {
		return fmt.Errorf("supervise: jwt wrap: %w", sbErr)
	}
	l.store.setToken(sb)

	exp, _ := time.Parse(time.RFC3339, resp.ExpiresAt)
	l.sessionMu.Lock()
	l.sessionExp = exp
	l.sessionJTI = resp.JTI
	l.sessionMu.Unlock()

	l.inputs.sessionExp.Store(&exp)
	jti := resp.JTI
	l.inputs.sessionJTI.Store(&jti)
	scopeCopy := append([]string(nil), l.config.Scope...)
	l.inputs.scopeHealthy.Store(&scopeCopy)
	emptyStale := []string{}
	l.inputs.scopeStale.Store(&emptyStale)

	l.emitSessionClaimed(ctx, resp.JTI, exp, l.config.Scope)
	return nil
}

// buildClaimPayload assembles the signed payload using the configured
// supervisor metadata and randomized nonce / request_id. SupervisorName
// is populated from the supervisor config [name] field — the server
// requires it for session_type=supervisor.
func (l *Lifecycle) buildClaimPayload() claimSignedPayload {
	return claimSignedPayload{
		EphemeralPubKey: l.deps.EphemeralPubKeyHex,
		MachineName:     l.deps.MachineName,
		Nonce:           l.deps.NonceFn(),
		Reason:          l.config.Reason,
		RequestID:       l.deps.RequestIDFn(),
		Scope:           append([]string(nil), l.config.Scope...),
		SessionType:     l.config.SessionType,
		SupervisorName:  l.config.Name,
		Timestamp:       l.deps.NowFn().UTC().Format(time.RFC3339Nano),
		TTL:             l.config.RequestedTTL.String(),
	}
}

// signAndWrapClaim canonicalises payload via sign.CanonicalJSON, signs with
// the client key, and assembles the wire envelope.
func signAndWrapClaim(ctx context.Context, clientKey *ecdsa.PrivateKey, fp string, payload claimSignedPayload) (claimWireRequest, error) {
	canonical, err := sign.CanonicalJSON(payload)
	if err != nil {
		return claimWireRequest{}, fmt.Errorf("supervise: canonical: %w", err)
	}
	sig, err := sign.Sign(ctx, clientKey, canonical)
	if err != nil {
		return claimWireRequest{}, fmt.Errorf("supervise: sign: %w", err)
	}
	return claimWireRequest{
		Scope:                payload.Scope,
		Reason:               payload.Reason,
		TTL:                  payload.TTL,
		SessionType:          payload.SessionType,
		EphemeralPubKey:      payload.EphemeralPubKey,
		Nonce:                payload.Nonce,
		Timestamp:            payload.Timestamp,
		Signature:            base64.StdEncoding.EncodeToString(sig),
		RequestID:            payload.RequestID,
		MachineName:          payload.MachineName,
		SupervisorName:       payload.SupervisorName,
		ClientKeyFingerprint: fp,
	}, nil
}

// jitterInterval applies ±20% jitter to d. Cap is enforced at bootBackoffCap.
func jitterInterval(d time.Duration) time.Duration {
	if d <= 0 {
		return bootBackoffInitial
	}
	if d > bootBackoffCap {
		d = bootBackoffCap
	}
	// ±20% jitter via crypto/rand to avoid reseeding math/rand.
	// max = 40% of d; sample uniformly from [-20%, +20%].
	maxJ := big.NewInt(int64(d) / 5)
	if maxJ.Sign() <= 0 {
		return d
	}
	r, err := rand.Int(rand.Reader, maxJ)
	if err != nil {
		return d
	}
	jitter := time.Duration(r.Int64() - int64(d)/10)
	if jitter+d < 0 {
		return d
	}
	out := d + jitter
	if out > bootBackoffCap {
		out = bootBackoffCap
	}
	return out
}

// defaultVaultHzProbe returns a probe that issues GET <serverURL>/hz with
// a 2s timeout. Returns nil iff the server responds 200.
func defaultVaultHzProbe(client *http.Client) func(ctx context.Context, serverURL string) error {
	return func(ctx context.Context, serverURL string) error {
		target := strings.TrimRight(serverURL, "/") + "/hz"
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return fmt.Errorf("supervise: /hz request: %w", err)
		}
		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("supervise: /hz: %w", err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("%w: status=%d", errHzNon200, resp.StatusCode)
		}
		return nil
	}
}

// defaultNonceFn returns a 43-character base64url-encoded random nonce.
func defaultNonceFn() string {
	b := make([]byte, 32)
	_, _ = io.ReadFull(rand.Reader, b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// defaultRequestIDFn returns a 32-character base64url-encoded random ID.
func defaultRequestIDFn() string {
	b := make([]byte, 24)
	_, _ = io.ReadFull(rand.Reader, b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// compressedEphemeralPubHex returns the SEC1-compressed 33-byte form of pub
// hex-lowercase-encoded to 66 chars.
//
//nolint:staticcheck // secp256k1 unsupported by crypto/ecdh; .X/.Y are read-only here
func compressedEphemeralPubHex(pub *ecdsa.PublicKey) string {
	if pub == nil || pub.X == nil || pub.Y == nil {
		return ""
	}
	out := make([]byte, 33)
	if pub.Y.Bit(0) == 0 {
		out[0] = 0x02
	} else {
		out[0] = 0x03
	}
	xb := pub.X.Bytes()
	copy(out[1+32-len(xb):], xb)
	return hex.EncodeToString(out)
}

// clientKeyFingerprintHex returns the 16-character lowercase hex client-key
// fingerprint matching internal/keys/fingerprint.go's PublicKeyFingerprint.
// The orchestrator wires this seam from cli when known; tests can override.
func clientKeyFingerprintHex(pub *ecdsa.PublicKey) string {
	if pub == nil || pub.X == nil || pub.Y == nil { //nolint:staticcheck // secp256k1 unsupported by crypto/ecdh
		return ""
	}
	cp := compressedEphemeralPubHex(pub)
	if cp == "" {
		return ""
	}
	raw, err := hex.DecodeString(cp)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:8])
}
