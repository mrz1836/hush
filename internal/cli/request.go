// Package cli — `hush request` subcommand: interactive secret fetch.
//
// Mounts on the cobra root via newRequestCmd() (no new
// exported package-level symbols). Two delivery modes:
//   - --exec <program>: run a child with the requested secrets in its
//     environment; child exit code becomes the parent's exit code.
//   - --format eval:    print one `export NAME='value'` line per scope to
//     stdout PLUS a locked stderr WARNING per docs/SECURITY.md §6.
//
// The two are mutually exclusive at the input-validation layer; neither
// → ExitInputErr before any keychain or network call.
package cli

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"

	secp256k1 "github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/spf13/cobra"

	"github.com/mrz1836/hush/internal/keychain"
	"github.com/mrz1836/hush/internal/keys"
	"github.com/mrz1836/hush/internal/transport/ecies"
	"github.com/mrz1836/hush/internal/transport/sign"
	"github.com/mrz1836/hush/internal/vault/securebytes"
)

// Locked stderr literals — every byte is contract-asserted.
const (
	requestMsgKeychainMissFmt    = "hush: request: client key not found in keychain — run `hush init client --machine-index %d` first"
	requestMsgKeychainPermDenied = "hush: request: keychain access denied (per-binary ACL refused)"
	requestMsgTransportFmt       = "hush: request: could not connect to hush server at %s: %s"
	requestMsgDeniedDiscord      = "hush: request: approval denied on Discord"
	requestMsgServerTimeout      = "hush: request: server reported approval timeout"
	requestMsgClientDeadline     = "hush: request: approval wait exceeded --ttl"
	requestMsgDiscordUnavailable = "hush: request: Discord bot unavailable; vault server returned 503"
	requestMsgPartialFetchFmt    = "hush: request: secret %q not present in vault; aborting before child start\n  if you added it after hush serve started, run serve with --reload-on-vault-change or send SIGHUP to reload the vault."
	requestMsgInterrupted        = "hush: request: interrupted; pending request will expire server-side at --ttl"
	requestMsgRateLimited        = "hush: request: rate limited; retry shortly"
	requestMsgBadSignature       = "hush: request: server rejected client key signature"
	requestMsgBadRequest         = "hush: request: server rejected request shape"
	requestMsgServerStatusFmt    = "hush: request: server returned %d at %s"
	requestMsgUnauthFetchFmt     = "hush: request: server rejected JWT for scope %q"
	requestMsgOutOfScopeFmt      = "hush: request: scope %q not in approved set"
)

// Flag-name constants used by the request subcommand.
const (
	flagReqServer        = "server"
	flagReqScope         = "scope"
	flagReqReason        = "reason"
	flagReqTTL           = "ttl"
	flagReqMaxUses       = "max-uses"
	flagReqMachineIndex  = "machine-index"
	flagReqExec          = "exec"
	flagReqFormat        = "format"
	flagReqClientKeyFile = "client-key-file"
)

// hostnameSanitiseRe matches characters allowed in machine_name per
// the server's regex; chars not in this set are stripped.
var hostnameSanitiseRe = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// scopeNameRE accepts POSIX-portable shell identifiers — a leading
// letter or underscore followed by 0..255 letter/digit/underscore. The
// invariant guards `--format eval` output (where scope names are
// embedded into the operator's shell verbatim) and prevents a
// malformed name from masquerading as a shell-control sequence.
var scopeNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,255}$`)

// validateScopeNames refuses any --scope entry that fails scopeNameRE.
// Returns errInvalidScopeName for the first offender; the error wraps
// the violating value so tests can assert on it.
func validateScopeNames(names []string) error {
	for _, n := range names {
		if !scopeNameRE.MatchString(n) {
			return fmt.Errorf("%w (got %q)", errInvalidScopeName, n)
		}
	}
	return nil
}

// formatModeEval is the only literal accepted by --format.
const formatModeEval = "eval"

// Static error sentinels for runtime failure paths. Wrapped via fmt.Errorf
// so callers can errors.Is them.
var (
	errClientKeyLength    = errors.New("hush/cli: request: client key length unexpected")
	errEmptyJWT           = errors.New("hush/cli: request: empty jwt in response")
	errServerApproval     = errors.New("hush/cli: request: server approval timeout")
	errRateLimited        = errors.New("hush/cli: request: rate limited")
	errDiscordUnavailable = errors.New("hush/cli: request: discord unavailable")
	errServerStatus       = errors.New("hush/cli: request: server returned non-2xx")
)

// requestFlags is the parsed-and-validated flag-layer state.
type requestFlags struct {
	server        string
	scope         []string
	reason        string
	ttl           time.Duration
	maxUses       int
	machineIndex  uint32
	clientKeyFile string
	execProgram   string
	formatMode    string
	childArgs     []string
}

// modeOf reports which delivery mode the validated flags select. One
// of "exec", "eval".
func (f requestFlags) modeOf() string {
	if f.execProgram != "" {
		return "exec"
	}
	return formatModeEval
}

// claimWireRequest mirrors internal/server/claim_handler.go::claimRequest
// exactly — same JSON tags, same field set. SupervisorName is empty for
// interactive callers (omitempty keeps it absent on the wire).
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

// claimSignedPayload mirrors the server's signedPayload exactly. The
// alphabetical-tag fields produce a byte-identical canonical encoding
// via sign.CanonicalJSON. CanonicalJSON ignores omitempty — both client
// and server emit `"supervisor_name":""` for interactive sessions, so
// the signatures match regardless of whether the wire envelope carries
// the empty field.
type claimSignedPayload struct {
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

// requestDeps groups the testable seams. Tests substitute deterministic
// replacements (FakeKeychain, fixed-time clock, recorded runner, etc.).
type requestDeps struct {
	keychain     keychain.Keychain
	httpClient   *http.Client
	nowFn        func() time.Time
	randReader   io.Reader
	hostnameFn   func() (string, error)
	ephemeralKey func(io.Reader) (*ecdsa.PrivateKey, error)
	looker       func(string) (string, error)
	runner       func(*exec.Cmd) error
	signalCtx    func(parent context.Context, sigs ...os.Signal) (context.Context, context.CancelFunc)
}

// productionRequestDeps returns the locked production wiring.
func productionRequestDeps() (requestDeps, error) {
	kc, err := keychain.New(nil)
	if err != nil {
		return requestDeps{}, err
	}
	return requestDeps{
		keychain: kc,
		httpClient: &http.Client{
			Transport: &http.Transport{
				DisableKeepAlives:   true,
				MaxIdleConnsPerHost: 1,
			},
		},
		nowFn:        time.Now,
		randReader:   rand.Reader,
		hostnameFn:   os.Hostname,
		ephemeralKey: generateEphemeralKey,
		looker:       exec.LookPath,
		runner:       func(cmd *exec.Cmd) error { return cmd.Run() },
		signalCtx:    signal.NotifyContext,
	}, nil
}

// newRequestCmd builds the cobra `request` subcommand.
func newRequestCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "request",
		Short: "Sign and submit an interactive secret claim; deliver via --exec or --format eval",
		RunE: func(cmd *cobra.Command, args []string) error {
			out := outputFromCmd(cmd)
			flags, err := parseAndValidateFlags(cmd, args)
			if err != nil {
				emitValidationStderr(out.stderr, err)
				return err
			}
			deps, err := productionRequestDeps()
			if err != nil {
				return err
			}
			return runRequest(cmd.Context(), out.stdout, out.stderr, deps, flags)
		},
	}

	cmd.Flags().String(flagReqServer, "", "Server URL (required)")
	cmd.Flags().StringSlice(flagReqScope, nil, "Comma-separated scope names (required)")
	cmd.Flags().String(flagReqReason, "", "Operator-provided reason; visible to the approver (required)")
	cmd.Flags().Duration(flagReqTTL, 0, "Approval-wait deadline AND issued JWT TTL (required)")
	cmd.Flags().Int(flagReqMaxUses, 0, "Max times the issued JWT can fetch a secret (required, ≥ len(scope))")
	cmd.Flags().Uint32(flagReqMachineIndex, 0, "Per-machine identifier matching `hush init client --machine-index <N>` (required)")
	cmd.Flags().String(flagReqClientKeyFile, "", "Optional 0600 smoke-test client key file used when macOS Keychain reads are unavailable")
	cmd.Flags().String(flagReqExec, "", "Program to exec with secrets injected as env vars (mutually exclusive with --format)")
	cmd.Flags().String(flagReqFormat, "", "Delivery format; only literal value `eval` accepted (mutually exclusive with --exec)")

	return cmd
}

// emitValidationStderr writes the locked stderr message for a known
// flag-validation sentinel. Unknown errors fall through to caller-side
// rendering.
func emitValidationStderr(stderr *Stream, err error) {
	switch {
	case errors.Is(err, errMissingFlag):
		_ = stderr.WriteText("hush: request: missing required flag(s): %s", strings.TrimPrefix(err.Error(), errMissingFlag.Error()+": "))
	case errors.Is(err, errMissingExecOrFormat):
		_ = stderr.WriteText("hush: request: must specify --exec or --format eval")
	case errors.Is(err, errExecAndFormatBothSet):
		_ = stderr.WriteText("hush: request: --exec and --format eval are mutually exclusive")
	case errors.Is(err, errFormatNotEval):
		_ = stderr.WriteText(`hush: request: --format only accepts the literal value "eval"`)
	case errors.Is(err, errMaxUsesTooLow):
		_ = stderr.WriteText("hush: request: --max-uses must be ≥ number of scopes")
	}
}

// parseAndValidateFlags is a pure-function validator (no I/O on any
// path) — this property is locked by tests T075–T079.
//
//nolint:gocognit,gocyclo // sequential per-flag pull + mutual-exclusion switch
func parseAndValidateFlags(cmd *cobra.Command, args []string) (requestFlags, error) {
	server, _ := cmd.Flags().GetString(flagReqServer)
	scopeRaw, _ := cmd.Flags().GetStringSlice(flagReqScope)
	reason, _ := cmd.Flags().GetString(flagReqReason)
	ttl, _ := cmd.Flags().GetDuration(flagReqTTL)
	maxUses, _ := cmd.Flags().GetInt(flagReqMaxUses)
	maxUsesSet := cmd.Flags().Changed(flagReqMaxUses)
	machineIndex, _ := cmd.Flags().GetUint32(flagReqMachineIndex)
	machineIndexSet := cmd.Flags().Changed(flagReqMachineIndex)
	clientKeyFile, _ := cmd.Flags().GetString(flagReqClientKeyFile)
	execProgram, _ := cmd.Flags().GetString(flagReqExec)
	formatMode, _ := cmd.Flags().GetString(flagReqFormat)

	// Cobra's StringSlice may yield comma-joined entries; split fully.
	scope := make([]string, 0, len(scopeRaw))
	for _, raw := range scopeRaw {
		for item := range strings.SplitSeq(raw, ",") {
			item = strings.TrimSpace(item)
			if item == "" {
				continue
			}
			scope = append(scope, item)
		}
	}

	flags := requestFlags{
		server:        strings.TrimSpace(server),
		scope:         scope,
		reason:        reason,
		ttl:           ttl,
		maxUses:       maxUses,
		machineIndex:  machineIndex,
		clientKeyFile: strings.TrimSpace(clientKeyFile),
		execProgram:   execProgram,
		formatMode:    formatMode,
	}

	if missing := missingRequestFlags(flags, maxUsesSet, machineIndexSet); len(missing) > 0 {
		return flags, fmt.Errorf("%w: %s", errMissingFlag, strings.Join(missing, ", "))
	}

	// Mutual exclusion comes first — neither vs both. No I/O on any
	// failure path.
	switch {
	case execProgram == "" && formatMode == "":
		return flags, errMissingExecOrFormat
	case execProgram != "" && formatMode != "":
		return flags, errExecAndFormatBothSet
	case formatMode != "" && formatMode != formatModeEval:
		return flags, errFormatNotEval
	}

	if maxUses < len(scope) {
		return flags, errMaxUsesTooLow
	}

	if err := validateScopeNames(scope); err != nil {
		return flags, err
	}

	// Trailing positional argv after `--` becomes child argv[1:].
	if dash := cmd.ArgsLenAtDash(); dash >= 0 && dash <= len(args) {
		flags.childArgs = append(flags.childArgs, args[dash:]...)
	}

	return flags, nil
}

func missingRequestFlags(flags requestFlags, maxUsesSet, machineIndexSet bool) []string {
	missing := make([]string, 0, 6)
	if flags.server == "" {
		missing = append(missing, "--"+flagReqServer)
	}
	if len(flags.scope) == 0 {
		missing = append(missing, "--"+flagReqScope)
	}
	if strings.TrimSpace(flags.reason) == "" {
		missing = append(missing, "--"+flagReqReason)
	}
	if flags.ttl <= 0 {
		missing = append(missing, "--"+flagReqTTL)
	}
	if !maxUsesSet {
		missing = append(missing, "--"+flagReqMaxUses)
	}
	if !machineIndexSet {
		missing = append(missing, "--"+flagReqMachineIndex)
	}
	return missing
}

// runRequest is the orchestration entry point. State transitions:
//
//  1. retrieveClientKey
//  2. generateEphemeralKey
//  3. buildClaimPayload + signAndWrapClaim
//  4. POST /claim under signal.NotifyContext + ttl deadline
//  5. fetchSecrets (all-or-nothing)
//  6. mode dispatch: runChild OR writeEvalExports
//  7. defer: zero ephemeral D, Destroy JWT SB, Destroy each secret SB,
//     cancel signal ctx
//
//nolint:gocognit,gocyclo,cyclop,funlen // sequential pipeline; complexity is structural
func runRequest(parentCtx context.Context, stdout, stderr *Stream, deps requestDeps, flags requestFlags) error {
	// Fail fast before signing, Discord approval, or secret fetch when --exec
	// is not a resolvable program. This prevents burning an approval on a
	// child-command typo such as --exec 'printenv HUSH_SMOKE_TEST'.
	if flags.modeOf() == "exec" {
		if _, err := preflightExecProgram(deps, flags.execProgram, stderr); err != nil {
			return err
		}
	}

	// 1. Client signing key.
	clientKey, err := retrieveClientKey(parentCtx, deps, flags.machineIndex, flags.clientKeyFile, stderr)
	if err != nil {
		return err
	}
	defer zeroPrivateKey(clientKey)

	// 2. Ephemeral key (one per request).
	ephPriv, err := deps.ephemeralKey(deps.randReader)
	if err != nil {
		return fmt.Errorf("hush/cli: request: ephemeral key: %w", err)
	}
	defer zeroPrivateKey(ephPriv)

	// 3. Build canonical claim + sign + wrap.
	ephHex := compressedEphemeralPubHex(ephPriv)
	payload, err := buildClaimPayload(flags, ephHex, deps)
	if err != nil {
		return err
	}
	wire, err := signAndWrapClaim(parentCtx, clientKey, payload)
	if err != nil {
		return err
	}

	// 4. Signal-aware context bounded by --ttl.
	sigCtx, sigCancel := deps.signalCtx(parentCtx, syscall.SIGINT, syscall.SIGTERM)
	defer sigCancel()
	deadlineCtx, deadlineCancel := context.WithDeadline(sigCtx, deps.nowFn().Add(flags.ttl))
	defer deadlineCancel()

	resp, err := postClaim(deadlineCtx, deps, flags.server, wire, stderr)
	if err != nil {
		return err
	}

	// JWT held in a SecureBytes for symmetry with the secrets.
	jwtSB, err := securebytes.New([]byte(resp.JWT))
	if err != nil {
		return fmt.Errorf("hush/cli: request: jwt secure-wrap: %w", err)
	}
	defer func() { _ = jwtSB.Destroy() }()

	// 5. Fetch all secrets BEFORE either delivery mode runs.
	secrets, fetchErr := fetchSecrets(deadlineCtx, deps, flags.server, jwtSB, ephPriv, flags.scope, stderr)
	defer func() {
		for _, sb := range secrets {
			if sb != nil {
				_ = sb.Destroy()
			}
		}
	}()
	if fetchErr != nil {
		return fetchErr
	}

	// 6. Mode dispatch.
	switch flags.modeOf() {
	case "exec":
		env, envErr := buildChildEnv(flags.scope, secrets, os.Environ())
		if envErr != nil {
			return envErr
		}
		return runChild(parentCtx, deps, flags.execProgram, flags.childArgs, env, stderr)
	case formatModeEval:
		return writeEvalExports(stdout, stderr, flags.scope, secrets)
	default:
		return errMissingExecOrFormat
	}
}

// retrieveClientKey loads the per-machine client signing scalar from
// the OS keychain and reconstitutes an *ecdsa.PrivateKey. The
// sanitized SecureBytes handle is Destroyed inside the same call; the
// reconstituted key is zeroed by the caller's defer chain.
func retrieveClientKey(ctx context.Context, deps requestDeps, machineIndex uint32, clientKeyFile string, stderr *Stream) (*ecdsa.PrivateKey, error) {
	if path := strings.TrimSpace(clientKeyFile); path != "" {
		return retrieveClientKeyFromFile(path)
	}
	account := fmt.Sprintf("machine-%d", machineIndex)
	sb, err := deps.keychain.Retrieve(ctx, kcServiceClient, account)
	if err != nil {
		switch {
		case errors.Is(err, keychain.ErrKeychainItemNotFound):
			_ = stderr.WriteText(requestMsgKeychainMissFmt, machineIndex)
		case errors.Is(err, keychain.ErrKeychainPermissionDenied):
			_ = stderr.WriteText(requestMsgKeychainPermDenied)
		}
		return nil, err
	}
	defer func() { _ = sb.Destroy() }()

	var (
		priv  *ecdsa.PrivateKey
		useEr error
	)
	if uerr := sb.Use(func(b []byte) {
		scalar, decErr := keychain.DecodeFixedBinary(b, 32)
		if decErr != nil {
			useEr = fmt.Errorf("%w: %d, want 32", errClientKeyLength, len(b))
			return
		}
		k := secp256k1.PrivKeyFromBytes(scalar)
		priv = k.ToECDSA()
		// Zero the local scratch buffer; the *ecdsa.PrivateKey holds
		// its own copy of the scalar in priv.D.
		for i := range scalar {
			scalar[i] = 0
		}
	}); uerr != nil {
		return nil, uerr
	}
	if useEr != nil {
		return nil, useEr
	}
	return priv, nil
}

func retrieveClientKeyFromFile(path string) (*ecdsa.PrivateKey, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // operator-supplied smoke-test key path
	if err != nil {
		return nil, fmt.Errorf("hush/cli: request: read client key file: %w", err)
	}
	decoded, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		return nil, fmt.Errorf("hush/cli: request: decode client key file: %w", err)
	}
	if len(decoded) != 32 {
		return nil, fmt.Errorf("%w: %d, want 32", errClientKeyLength, len(decoded))
	}
	scalar := make([]byte, 32)
	copy(scalar, decoded)
	k := secp256k1.PrivKeyFromBytes(scalar)
	for i := range scalar {
		scalar[i] = 0
	}
	for i := range decoded {
		decoded[i] = 0
	}
	return k.ToECDSA(), nil
}

// generateEphemeralKey produces a fresh secp256k1 keypair used for
// per-request ECIES envelopes. The randReader argument is preserved
// for symmetry with the deps seam — secp256k1.GeneratePrivateKey reads
// from crypto/rand internally.
func generateEphemeralKey(_ io.Reader) (*ecdsa.PrivateKey, error) {
	k, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		return nil, fmt.Errorf("hush/cli: request: generate ephemeral key: %w", err)
	}
	return k.ToECDSA(), nil
}

// compressedEphemeralPubHex returns the SEC1-compressed 33-byte form
// of priv's public key, hex-lowercase-encoded to 66 chars.
func compressedEphemeralPubHex(priv *ecdsa.PrivateKey) string {
	out := make([]byte, 33)
	//nolint:staticcheck // secp256k1 unsupported by crypto/ecdh; .X/.Y are read-only here
	if priv.PublicKey.Y.Bit(0) == 0 {
		out[0] = 0x02
	} else {
		out[0] = 0x03
	}
	//nolint:staticcheck // see above
	xb := priv.PublicKey.X.Bytes()
	copy(out[1+32-len(xb):], xb)
	return hex.EncodeToString(out)
}

// buildClaimPayload assembles the nine-field signed payload. Generates
// nonce (32 random bytes → 43-char base64url), request_id (24 random
// bytes → 32-char base64url), timestamp (RFC3339Nano UTC), and the
// sanitized machine_name truncated to 64 chars.
func buildClaimPayload(flags requestFlags, ephHex string, deps requestDeps) (claimSignedPayload, error) {
	nonceRaw := make([]byte, 32)
	if _, err := io.ReadFull(deps.randReader, nonceRaw); err != nil {
		return claimSignedPayload{}, fmt.Errorf("hush/cli: request: nonce: %w", err)
	}
	requestIDRaw := make([]byte, 24)
	if _, err := io.ReadFull(deps.randReader, requestIDRaw); err != nil {
		return claimSignedPayload{}, fmt.Errorf("hush/cli: request: request_id: %w", err)
	}
	host, err := deps.hostnameFn()
	if err != nil || host == "" {
		host = "unknown"
	}
	host = sanitiseMachineName(host)

	return claimSignedPayload{
		EphemeralPubKey: ephHex,
		MachineName:     host,
		Nonce:           base64.RawURLEncoding.EncodeToString(nonceRaw),
		Reason:          flags.reason,
		RequestID:       base64.RawURLEncoding.EncodeToString(requestIDRaw),
		Scope:           append([]string(nil), flags.scope...),
		SessionType:     "interactive",
		Timestamp:       deps.nowFn().UTC().Format(time.RFC3339Nano),
		TTL:             flags.ttl.String(),
	}, nil
}

// sanitiseMachineName strips characters not in [A-Za-z0-9._-] and
// truncates to 64 bytes to match the server's machine_name regex.
func sanitiseMachineName(in string) string {
	out := hostnameSanitiseRe.ReplaceAllString(in, "-")
	if out == "" {
		out = "unknown"
	}
	if len(out) > 64 {
		out = out[:64]
	}
	return out
}

// signAndWrapClaim canonicalises payload via sign.CanonicalJSON, signs
// with the client key, and assembles the twelve-key wire envelope.
func signAndWrapClaim(ctx context.Context, clientKey *ecdsa.PrivateKey, payload claimSignedPayload) (claimWireRequest, error) {
	canonical, err := sign.CanonicalJSON(payload)
	if err != nil {
		return claimWireRequest{}, fmt.Errorf("hush/cli: request: canonical: %w", err)
	}
	sig, err := sign.Sign(ctx, clientKey, canonical)
	if err != nil {
		return claimWireRequest{}, fmt.Errorf("hush/cli: request: sign: %w", err)
	}
	fp := keys.PublicKeyFingerprint(&clientKey.PublicKey)
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
		ClientKeyFingerprint: fp,
	}, nil
}

// postClaim POSTs the wire envelope to <server>/claim and decodes the
// response. Maps server-side error codes to actionable sentinel errors
// per contract §6.
//
//nolint:cyclop // sequential build → POST → status branch
func postClaim(ctx context.Context, deps requestDeps, server string, body claimWireRequest, stderr *Stream) (claimWireResponse, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return claimWireResponse{}, fmt.Errorf("hush/cli: request: marshal claim: %w", err)
	}
	target := strings.TrimRight(server, "/") + "/claim"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(raw))
	if err != nil {
		return claimWireResponse{}, fmt.Errorf("hush/cli: request: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := deps.httpClient.Do(req)
	if err != nil {
		// Translate cancellation/deadline into the locked stderr text;
		// fall back to the transport classifier for anything else.
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			_ = stderr.WriteText(requestMsgClientDeadline)
		case errors.Is(err, context.Canceled):
			_ = stderr.WriteText(requestMsgInterrupted)
		default:
			_ = stderr.WriteText(requestMsgTransportFmt, server, classifyTransportErr(err))
		}
		return claimWireResponse{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))

	if resp.StatusCode == http.StatusOK {
		var ok claimWireResponse
		if jerr := json.Unmarshal(bodyBytes, &ok); jerr != nil {
			return claimWireResponse{}, fmt.Errorf("hush/cli: request: decode claim response: %w", jerr)
		}
		if ok.JWT == "" {
			return claimWireResponse{}, errEmptyJWT
		}
		return ok, nil
	}

	var errBody claimWireError
	_ = json.Unmarshal(bodyBytes, &errBody)
	return claimWireResponse{}, mapClaimErrorCode(resp.StatusCode, errBody.Error, stderr, server)
}

// mapClaimErrorCode maps a non-200 /claim response to the appropriate
// sentinel + locked stderr line.
//
//nolint:cyclop // direct table dispatch over the contract codes
func mapClaimErrorCode(status int, code string, stderr *Stream, server string) error {
	switch code {
	case "denied":
		_ = stderr.WriteText(requestMsgDeniedDiscord)
		return errAuthFailed
	case "bad_signature":
		_ = stderr.WriteText(requestMsgBadSignature)
		return errAuthFailed
	case "approval_timeout":
		_ = stderr.WriteText(requestMsgServerTimeout)
		return errServerApproval
	case "rate_limited":
		_ = stderr.WriteText(requestMsgRateLimited)
		return errRateLimited
	case "discord_unavailable":
		_ = stderr.WriteText(requestMsgDiscordUnavailable)
		return errDiscordUnavailable
	case "bad_request", "stale_timestamp", "nonce_replay", "ip_not_allowed":
		_ = stderr.WriteText(requestMsgBadRequest)
		return fmt.Errorf("hush/cli: request: %w: %s", errMissingFlag, code)
	}
	_ = stderr.WriteText(requestMsgServerStatusFmt, status, server)
	return fmt.Errorf("%w: %d", errServerStatus, status)
}

// fetchSecrets GETs each scope name in turn under the issued JWT and
// ECIES-decrypts the body. All-or-nothing: any per-scope failure
// returns the partially-populated slice (caller's defer destroys it)
// AND the mapped error.
//
//nolint:gocognit,gocyclo,cyclop // sequential fetch loop with per-status branches
func fetchSecrets(
	ctx context.Context,
	deps requestDeps,
	server string,
	jwt *securebytes.SecureBytes,
	ephPriv *ecdsa.PrivateKey,
	scope []string,
	stderr *Stream,
) ([]*securebytes.SecureBytes, error) {
	out := make([]*securebytes.SecureBytes, 0, len(scope))
	for _, name := range scope {
		target := strings.TrimRight(server, "/") + "/s/" + name
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return out, fmt.Errorf("hush/cli: request: build secret request: %w", err)
		}
		// Set the Authorization header inside a Use callback so the JWT
		// bytes do not escape into a long-lived string variable.
		var bearerErr error
		if uerr := jwt.Use(func(b []byte) {
			req.Header.Set("Authorization", "Bearer "+string(b))
		}); uerr != nil {
			bearerErr = uerr
		}
		if bearerErr != nil {
			return out, bearerErr
		}

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
			return out, err
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
		_ = resp.Body.Close()
		if readErr != nil {
			return out, fmt.Errorf("hush/cli: request: read secret body: %w", readErr)
		}

		switch resp.StatusCode {
		case http.StatusOK:
			sb, derr := ecies.Decrypt(ctx, ephPriv, body)
			if derr != nil {
				return out, fmt.Errorf("hush/cli: request: decrypt %s: %w", name, derr)
			}
			out = append(out, sb)
		case http.StatusUnauthorized:
			_ = stderr.WriteText(requestMsgUnauthFetchFmt, name)
			return out, errAuthFailed
		case http.StatusForbidden:
			_ = stderr.WriteText(requestMsgOutOfScopeFmt, name)
			return out, errAuthFailed
		case http.StatusNotFound:
			_ = stderr.WriteText(requestMsgPartialFetchFmt, name)
			return out, errNotFound
		default:
			_ = stderr.WriteText(requestMsgServerStatusFmt, resp.StatusCode, server)
			return out, fmt.Errorf("%w: %d for %s", errServerStatus, resp.StatusCode, name)
		}
	}
	return out, nil
}

// zeroPrivateKey clears the underlying *big.Int scalar. Safe on nil.
func zeroPrivateKey(priv *ecdsa.PrivateKey) {
	if priv == nil || priv.D == nil { //nolint:staticcheck // secp256k1 not in crypto/ecdh; .D field access is intentional
		return
	}
	//nolint:staticcheck // see above
	priv.D.SetBytes(make([]byte, 32))
}
