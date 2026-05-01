package cli

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// errRevokeUnexpectedStatus is the sentinel returned for HTTP
// statuses outside the locked 200/401/403/404 set.
var errRevokeUnexpectedStatus = errors.New("revoke: unexpected status")

// jtiRe is the UUID format expected on --jti. Mirrors the chassis's
// getRequestIDRe (the chassis treats jti and request_id with the
// same shape).
var jtiRe = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)

// revokeTotalTimeout reuses the same 5 s ceiling as `health` so
// operators get one number to reason about (FR-015a).
const revokeTotalTimeout = 5 * time.Second

// revokePayload is the canonical body signed by the client and
// posted to the server. SDD-08's CanonicalJSON yields the exact byte
// sequence the server re-canonicalises and verifies against.
type revokePayload struct {
	JTI       string `json:"jti"`
	Nonce     string `json:"nonce"`
	Timestamp string `json:"timestamp"`
}

// revokeRequest is the wire envelope: the canonical payload bytes
// (re-decoded server-side) plus the detached signature.
type revokeRequest struct {
	Payload   json.RawMessage `json:"payload"`
	Signature string          `json:"signature"`
}

// revokeResponseDoc is the success-shape echoed to a non-TTY stdout.
type revokeResponseDoc struct {
	Revoked string `json:"revoked"`
}

// revokeDeps groups the testable seams: signing-key source and HTTP
// transport. Production constructs these from the same passphrase
// path as `serve`; tests inject deterministic substitutes.
type revokeDeps struct {
	signKey *ecdsa.PrivateKey
	client  *http.Client
	now     func() time.Time
	rand    io.Reader
}

func newRevokeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "revoke",
		Short: "Revoke an active session token",
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := outputFromCmd(cmd)
			server, _ := cmd.Flags().GetString("server")
			jti, _ := cmd.Flags().GetString("jti")
			if server == "" {
				_ = out.stderr.WriteText("missing required flag: --server")
				return fmtError(errMissingFlag, "--server")
			}
			if jti == "" {
				_ = out.stderr.WriteText("missing required flag: --jti")
				return fmtError(errMissingFlag, "--jti")
			}
			if !jtiRe.MatchString(jti) {
				_ = out.stderr.WriteText("invalid --jti: must be a UUID")
				return errInvalidJTI
			}
			deps := revokeDeps{} // testable seam built lazily inside runRevoke
			return runRevoke(cmd.Context(), out.stdout, out.stderr, deps, server, jti)
		},
	}
	cmd.Flags().String("server", "", "Server URL (required)")
	cmd.Flags().String("jti", "", "Token ID to revoke (required, UUID)")
	return cmd
}

// runRevoke is the testable revoke hot path. Constructs the canonical
// signed payload, posts it, and maps the HTTP status to one of the
// locked exit codes via the returned error sentinels.
//
//nolint:gocognit,cyclop,gocyclo // sequential build→sign→POST→classify pipeline; branches map 1:1 to the locked HTTP-status → exit-code map
func runRevoke(ctx context.Context, stdout, stderr *Stream, deps revokeDeps, serverURL, jti string) error {
	if deps.now == nil {
		deps.now = time.Now
	}
	if deps.rand == nil {
		deps.rand = rand.Reader
	}
	if deps.client == nil {
		deps.client = &http.Client{
			Timeout: revokeTotalTimeout,
			Transport: &http.Transport{
				DisableKeepAlives:   true,
				MaxIdleConnsPerHost: 1,
			},
		}
	}
	if deps.signKey == nil {
		ephemeral, err := ephemeralRevokeKey(deps.rand)
		if err != nil {
			return fmt.Errorf("hush/cli: revoke: ephemeral key: %w", err)
		}
		deps.signKey = ephemeral
	}

	nonceBytes := make([]byte, 32)
	if _, err := io.ReadFull(deps.rand, nonceBytes); err != nil {
		return fmt.Errorf("hush/cli: revoke: nonce: %w", err)
	}
	payload := revokePayload{
		JTI:       jti,
		Nonce:     hex.EncodeToString(nonceBytes),
		Timestamp: deps.now().UTC().Format(time.RFC3339Nano),
	}

	canonical, err := canonicaliseRevokePayload(payload)
	if err != nil {
		return fmt.Errorf("hush/cli: revoke: canonical: %w", err)
	}

	signature, err := signRevokePayload(ctx, deps.signKey, canonical)
	if err != nil {
		return fmt.Errorf("hush/cli: revoke: sign: %w", err)
	}

	body, err := json.Marshal(revokeRequest{
		Payload:   json.RawMessage(canonical),
		Signature: signature,
	})
	if err != nil {
		return fmt.Errorf("hush/cli: revoke: encode envelope: %w", err)
	}

	target := strings.TrimRight(serverURL, "/") + "/revoke"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("hush/cli: revoke: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := deps.client.Do(req)
	if err != nil {
		_ = stderr.WriteText("could not connect to hush server at %s: %s", serverURL, classifyTransportErr(err))
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	excerpt, _ := io.ReadAll(io.LimitReader(resp.Body, 256))

	switch resp.StatusCode {
	case http.StatusOK:
		text := fmt.Sprintf("revoked jti=%s", jti)
		return stdout.Auto(text, revokeResponseDoc{Revoked: jti})
	case http.StatusUnauthorized, http.StatusForbidden:
		_ = stderr.WriteText("server rejected revocation: signature invalid (or jti unknown — server treats both alike)")
		return errAuthFailed
	case http.StatusNotFound:
		_ = stderr.WriteText("server reported jti not found: %s", jti)
		return errNotFound
	default:
		_ = stderr.WriteText("server returned %d: %s", resp.StatusCode, sanitiseExcerpt(excerpt))
		return fmt.Errorf("hush/cli: %w: %d", errRevokeUnexpectedStatus, resp.StatusCode)
	}
}

// sanitiseExcerpt replaces every byte outside [0x20, 0x7E] with '?'
// so a server-controlled body cannot smuggle ANSI sequences or
// terminal escapes through the CLI's stderr.
func sanitiseExcerpt(b []byte) string {
	out := make([]byte, len(b))
	for i, ch := range b {
		switch {
		case ch >= 0x20 && ch <= 0x7E:
			out[i] = ch
		default:
			out[i] = '?'
		}
	}
	return string(out)
}
