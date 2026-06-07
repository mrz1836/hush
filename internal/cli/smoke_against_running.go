package cli

import (
	"bytes"
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

const (
	smokeAgainstRunningReason = "hush smoke against-running proof"
	smokeMsgAgainstStart      = "hush: smoke: against-running proof requesting fake scope %s at %s"
	smokeMsgAgainstSuccess    = "hush: smoke: against-running proof passed — server reached approval path and rejected fake scope with %s"
)

func runSmokeAgainstRunning(ctx context.Context, stdout, stderr *Stream, deps smokeDeps, opts smokeOptions) error {
	if strings.TrimSpace(opts.clientKeyFile) == "" {
		_ = stderr.WriteText("hush: smoke: --client-key-file is required with --against-running")
		return fmt.Errorf("%w: --client-key-file", errMissingFlag)
	}
	serverURL, err := smokeAgainstRunningServerURL(ctx, deps, opts)
	if err != nil {
		return err
	}
	scope, err := newSmokeProofScope(deps.randReader)
	if err != nil {
		return err
	}
	_ = stderr.WriteText(smokeMsgAgainstStart, scope, serverURL)

	kc, err := deps.keychainFactory()
	if err != nil {
		return err
	}
	if destroyer, ok := kc.(interface{ Destroy() }); ok {
		defer destroyer.Destroy()
	}
	reqDeps := requestDeps{
		keychain:     kc,
		httpClient:   smokeAgainstRunningHTTPClient(deps.httpClient),
		nowFn:        deps.nowFn,
		randReader:   deps.randReader,
		hostnameFn:   os.Hostname,
		ephemeralKey: generateEphemeralKey,
		looker:       func(string) (string, error) { return "", nil },
		runner:       func(*exec.Cmd) error { return nil },
		signalCtx:    signal.NotifyContext,
	}
	outcome, err := smokePostClaimOnly(ctx, stderr, reqDeps, requestFlags{
		server:        serverURL,
		scope:         []string{scope},
		reason:        smokeAgainstRunningReason,
		ttl:           30 * time.Second,
		maxUses:       1,
		machineIndex:  opts.machineIndex,
		clientKeyFile: opts.clientKeyFile,
		formatMode:    formatModeEval,
		toolName:      "hush smoke --against-running",
	})
	if err != nil {
		return err
	}
	_ = stdout.WriteText(smokeMsgAgainstSuccess, outcome)
	return nil
}

func smokeAgainstRunningServerURL(ctx context.Context, deps smokeDeps, opts smokeOptions) (string, error) {
	configPath := opts.configPath
	if configPath == "" {
		configPath = defaultConfigPath()
	}
	expanded, err := expandTilde(configPath)
	if err != nil {
		return "", err
	}
	cfg, err := deps.configLoader(ctx, expanded)
	if err != nil {
		return "", fmt.Errorf("hush: smoke: load config %q: %w", configPath, err)
	}
	listenAddr := cfg.Server.ListenAddr.String()
	if strings.TrimSpace(opts.listenAddr) != "" {
		listenAddr = strings.TrimSpace(opts.listenAddr)
	}
	return "http://" + listenAddr + "/h/" + cfg.Server.PathPrefix, nil
}

func newSmokeProofScope(r io.Reader) (string, error) {
	if r == nil {
		r = crand.Reader
	}
	raw := make([]byte, 8)
	if _, err := io.ReadFull(r, raw); err != nil {
		return "", fmt.Errorf("hush: smoke: proof scope entropy: %w", err)
	}
	return smokeProofScopePrefix + strings.ToUpper(hex.EncodeToString(raw)), nil
}

func smokeAgainstRunningHTTPClient(client *http.Client) *http.Client {
	if client != nil {
		return client
	}
	return &http.Client{Timeout: 35 * time.Second}
}

func smokePostClaimOnly(ctx context.Context, stderr *Stream, deps requestDeps, flags requestFlags) (string, error) {
	clientKey, err := retrieveClientKey(ctx, deps, flags.machineIndex, flags.clientKeyFile, stderr)
	if err != nil {
		return "", err
	}
	defer zeroPrivateKey(clientKey)

	ephPriv, err := deps.ephemeralKey(deps.randReader)
	if err != nil {
		return "", fmt.Errorf("hush/cli: request: ephemeral key: %w", err)
	}
	defer zeroPrivateKey(ephPriv)

	payload, err := buildClaimPayload(flags, compressedEphemeralPubHex(ephPriv), deps)
	if err != nil {
		return "", err
	}
	wire, err := signAndWrapClaim(ctx, clientKey, payload)
	if err != nil {
		return "", err
	}

	sigCtx, sigCancel := deps.signalCtx(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer sigCancel()
	deadlineCtx, deadlineCancel := context.WithDeadline(sigCtx, deps.nowFn().Add(flags.ttl))
	defer deadlineCancel()

	code, err := postSmokeProofClaim(deadlineCtx, deps, flags.server, wire, stderr)
	if err != nil {
		return "", err
	}
	switch code {
	case "denied":
		return "denied", nil
	case "approval_timeout":
		return "approval_timeout", nil
	case "rate_limited":
		return "rate_limited", nil
	case "discord_unavailable":
		return "discord_unavailable", nil
	default:
		return "", fmt.Errorf("hush: smoke: against-running proof rejected before approval path: %s", code)
	}
}

func postSmokeProofClaim(ctx context.Context, deps requestDeps, server string, body claimWireRequest, stderr *Stream) (string, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("hush/cli: request: marshal claim: %w", err)
	}
	target := strings.TrimRight(server, "/") + "/claim"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(raw))
	if err != nil {
		return "", fmt.Errorf("hush/cli: request: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := deps.httpClient.Do(req)
	if err != nil {
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			_ = stderr.WriteText(requestMsgClientDeadline)
		case errors.Is(err, context.Canceled):
			_ = stderr.WriteText(requestMsgInterrupted)
		default:
			_ = stderr.WriteText(requestMsgTransportFmt, server, classifyTransportErr(err))
		}
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode == http.StatusOK {
		return "", errors.New("hush: smoke: against-running proof unexpectedly approved fake scope")
	}
	var errBody claimWireError
	if err := json.Unmarshal(bodyBytes, &errBody); err != nil {
		return "", fmt.Errorf("hush: smoke: decode proof claim response: %w", err)
	}
	if errBody.Error == "" {
		return "", fmt.Errorf("%w: %d", errServerStatus, resp.StatusCode)
	}
	return errBody.Error, nil
}
