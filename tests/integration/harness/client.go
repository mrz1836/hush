//go:build integration

package harness

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/mrz1836/hush/internal/keys"
	"github.com/mrz1836/hush/internal/transport/ecies"
	"github.com/mrz1836/hush/internal/transport/sign"
	"github.com/mrz1836/hush/internal/vault/securebytes"
)

// TestClient is a faithful interactive-claim client: it owns a machine
// signing key (registered with the vault) and a per-instance ephemeral
// ECIES keypair, signs a /claim exactly as `hush request` does, and
// fetches per-scope secrets via /s/<name> + ECIES decrypt. It is the
// harness counterpart of internal/cli/request.go, used by Scenario 1.
type TestClient struct {
	serverURL    string
	httpClient   *http.Client
	machineKey   *ecdsa.PrivateKey
	ephemeralKey *ecdsa.PrivateKey
	machineName  string
}

// ClientOpts configures NewClient. Vault and Server are required.
type ClientOpts struct {
	Vault        *TestVault
	Server       *TestServer
	MachineIndex uint32
	MachineName  string
}

// claimSignedPayloadJSON mirrors internal/server's signedPayload — the
// fields canonicalised and signed by the client. sign.CanonicalJSON sorts by
// JSON tag, so struct field order is irrelevant. SupervisorName is required
// for session_type=supervisor; CanonicalJSON ignores omitempty (always
// emits exported fields) so client and server canonical bytes match
// regardless of value.
type claimSignedPayloadJSON struct {
	EphemeralPubKey string   `json:"ephemeral_pubkey"`
	MachineName     string   `json:"machine_name"`
	Nonce           string   `json:"nonce"`
	Reason          string   `json:"reason"`
	RequestID       string   `json:"request_id"`
	Scope           []string `json:"scope"`
	SessionType     string   `json:"session_type"`
	SupervisorName  string   `json:"supervisor_name,omitempty"`
	Timestamp       string   `json:"timestamp"`
	TTL             string   `json:"ttl"`
}

// claimWireJSON mirrors internal/server's claimRequest — the POST /claim
// body. SupervisorName is omitted from the wire envelope when empty
// (interactive callers).
type claimWireJSON struct {
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

// claimResponseJSON mirrors internal/server's claimResponse.
type claimResponseJSON struct {
	JWT       string `json:"jwt"`
	ExpiresAt string `json:"expires_at"`
	JTI       string `json:"jti"`
}

// NewClient builds a TestClient, generating a machine key + ephemeral ECIES
// keypair and registering the machine public key with the vault's client
// registry so the in-process server can verify signed /claim payloads.
func NewClient(t *testing.T, opts ClientOpts) *TestClient {
	t.Helper()
	if opts.Vault == nil || opts.Server == nil {
		t.Fatal("harness.NewClient: Vault and Server are required")
	}
	name := opts.MachineName
	if name == "" {
		name = "interactive-client"
	}
	machineKey := NewECDSAKey(t)
	ephemeralKey := NewECDSAKey(t)
	opts.Vault.RegisterClient(t, opts.MachineIndex, &machineKey.PublicKey)
	return &TestClient{
		serverURL:    opts.Server.URL(),
		httpClient:   &http.Client{Timeout: 5 * time.Second},
		machineKey:   machineKey,
		ephemeralKey: ephemeralKey,
		machineName:  name,
	}
}

// Claim performs a signed interactive /claim and returns the issued JWT.
// It fails the test on any non-200 response.
func (c *TestClient) Claim(t *testing.T, ctx context.Context, scopes []string, ttl time.Duration, sessionType string) string {
	t.Helper()
	payload := claimSignedPayloadJSON{
		EphemeralPubKey: compressedPubKeyHex(&c.ephemeralKey.PublicKey),
		MachineName:     c.machineName,
		Nonce:           randomToken(43),
		Reason:          "harness interactive scenario",
		RequestID:       randomToken(32),
		Scope:           append([]string(nil), scopes...),
		SessionType:     sessionType,
		Timestamp:       time.Now().UTC().Format(time.RFC3339Nano),
		TTL:             ttl.String(),
	}
	canonical, err := sign.CanonicalJSON(payload)
	if err != nil {
		t.Fatalf("harness.Claim: canonical: %v", err)
	}
	sig, err := sign.Sign(ctx, c.machineKey, canonical)
	if err != nil {
		t.Fatalf("harness.Claim: sign: %v", err)
	}
	wire := claimWireJSON{
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
		ClientKeyFingerprint: keys.PublicKeyFingerprint(&c.machineKey.PublicKey),
	}
	raw, err := json.Marshal(wire)
	if err != nil {
		t.Fatalf("harness.Claim: marshal: %v", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.serverURL+"/claim", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("harness.Claim: build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		t.Fatalf("harness.Claim: POST /claim: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("harness.Claim: status %d: %s", resp.StatusCode, body)
	}
	var cr claimResponseJSON
	if err := json.Unmarshal(body, &cr); err != nil {
		t.Fatalf("harness.Claim: decode response: %v\nbody=%s", err, body)
	}
	if cr.JWT == "" {
		t.Fatalf("harness.Claim: empty JWT in response")
	}
	return cr.JWT
}

// FetchSecret performs GET /s/<name> with the bearer JWT and ECIES-decrypts
// the response under the client's ephemeral key. The caller owns the
// returned *SecureBytes and MUST Destroy it.
func (c *TestClient) FetchSecret(t *testing.T, ctx context.Context, name, jwt string) *securebytes.SecureBytes {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.serverURL+"/s/"+name, nil)
	if err != nil {
		t.Fatalf("harness.FetchSecret(%s): build request: %v", name, err)
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		t.Fatalf("harness.FetchSecret(%s): GET: %v", name, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("harness.FetchSecret(%s): status %d: %s", name, resp.StatusCode, body)
	}
	sb, err := ecies.Decrypt(ctx, c.ephemeralKey, body)
	if err != nil {
		t.Fatalf("harness.FetchSecret(%s): ECIES decrypt: %v", name, err)
	}
	return sb
}

// randomToken returns n random base64url characters — suitable for the
// nonce and request_id fields (regex [A-Za-z0-9_-]{16,64}).
func randomToken(n int) string {
	raw := make([]byte, n)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		panic("harness: randomToken: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(raw)[:n]
}
