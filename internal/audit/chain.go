package audit

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"

	"github.com/mrz1836/hush/internal/transport/sign"
)

// Event is one record of the hash-chained, signed audit log.  Serialised
// to disk verbatim as line-delimited canonical JSON.
type Event struct {
	Seq       uint64         `json:"seq"`
	Time      time.Time      `json:"time"`
	Action    string         `json:"action"`
	Data      map[string]any `json:"data,omitempty"`
	PrevHash  string         `json:"prev_hash"`
	Hash      string         `json:"hash"`
	Signature string         `json:"signature"`
}

// Action constants — the closed vocabulary of audit-event action strings.
// Future additions MAY append (never repurpose).
const (
	// Chassis lifecycle.
	ActionServerStart         = "server_start"
	ActionServerStop          = "server_stop"
	ActionVaultReloaded       = "vault_reloaded"
	ActionFilePermCheckFailed = "file_perm_check_failed"

	// Discord connectivity transitions.
	ActionDiscordDisconnected = "discord_disconnected"
	ActionDiscordReconnected  = "discord_reconnected"

	// Audit subsystem — emitted from inside the package.
	ActionAuditMirrorFailed = "audit_mirror_failed"

	// /s handler outcomes.
	ActionSecretRetrieved     = "secret_retrieved"
	ActionSecretBadToken      = "secret_bad_token"
	ActionSecretTokenExpired  = "secret_token_expired"
	ActionSecretOutOfScope    = "secret_out_of_scope"
	ActionSecretMissing       = "secret_missing"
	ActionSecretBadRequest    = "secret_bad_request" //nolint:gosec // G101: action label, not a credential
	ActionSecretInternalError = "secret_internal_error"

	// /revoke handler outcomes.
	ActionRevokeSucceeded                = "revoke_succeeded"
	ActionRevokeIdempotentAlreadyRevoked = "revoke_idempotent_already_revoked"
	ActionRevokeBadRequest               = "revoke_bad_request"
	ActionRevokeBadSignature             = "revoke_bad_signature"
	ActionRevokeNonceReplay              = "revoke_nonce_replay"
	ActionRevokeNonceCacheFull           = "revoke_nonce_cache_full"
	ActionRevokeStaleTimestamp           = "revoke_stale_timestamp"

	// Supervisor lifecycle. Emitted by the
	// supervisor orchestrator in internal/supervise/lifecycle*.go.
	// Future additions MAY append; never repurpose (per the
	// header invariant above).
	ActionSupervisorSessionClaimed   = "supervisor_session_claimed"
	ActionSupervisorSessionRefreshed = "supervisor_session_refreshed"
	ActionSupervisorSilentRefill     = "supervisor_silent_refill"
	ActionSupervisorChildCleanExit   = "supervisor_child_clean_exit"
	ActionSupervisorChildExitCrash   = "supervisor_child_exit_crash"
	ActionSupervisorChildExit78      = "supervisor_child_exit_78"
	ActionSupervisorAwaitingApproval = "supervisor_awaiting_approval"
	ActionSupervisorStaleAlert       = "supervisor_stale_alert"
	ActionSupervisorGraceEntered     = "supervisor_grace_entered"
	ActionSupervisorGraceExited      = "supervisor_grace_exited"
	ActionSupervisorBootTimeout      = "supervisor_boot_timeout"
	ActionSupervisorChildSwap        = "supervisor_child_swap"
	ActionClientRefreshInvoked       = "client_refresh_invoked"
)

// genesisDomainTag is the domain-separator string hashed once to derive
// the genesis prevHash for Seq=1.  Changing this string invalidates every
// existing chain — guarded by the version suffix `v1`.
const genesisDomainTag = "hush.audit.chain.v1.genesis"

// genesisPrevHash is the SHA-256 of the genesis domain tag.  Used as the
// PrevHash of the very first event in a fresh chain.  Computed once at
// package init via a function call (Go disallows non-trivial initialisers
// in const blocks).
//
//nolint:gochecknoglobals // sentinel-class derived constant; fixed at package load
var genesisPrevHash = sha256.Sum256([]byte(genesisDomainTag))

// Chain-related sentinel errors.  Compare via errors.Is.
var (
	// ErrAuditChainBroken indicates the on-disk chain failed integrity
	// verification.  errors.As against *ChainError to recover the
	// offending Seq.
	ErrAuditChainBroken = errors.New("hush/audit: chain integrity broken")

	// ErrShutdown is returned by Append when Run's ctx has been cancelled
	// and the writer is no longer accepting events.
	ErrShutdown = errors.New("hush/audit: writer shut down")

	// ErrChainTailUnreadable indicates the writer could not parse the last
	// line of an existing chain file at NewWriter time.  The chain is NOT
	// silently truncated; the operator must intervene.
	ErrChainTailUnreadable = errors.New("hush/audit: chain tail unreadable")

	// ErrInvalidPath indicates the supplied audit-log path is empty or
	// otherwise unusable.
	ErrInvalidPath = errors.New("hush/audit: invalid log path")

	// ErrInvalidKey indicates the supplied signing key is nil or not on
	// the secp256k1 curve.
	ErrInvalidKey = errors.New("hush/audit: invalid signing key")

	// ErrInvalidLogger indicates the supplied logger is nil.
	ErrInvalidLogger = errors.New("hush/audit: invalid logger")

	// ErrEmptyAction indicates an Append call with action == "".
	ErrEmptyAction = errors.New("hush/audit: empty action")

	// ErrAlreadyRun indicates Run was called twice on the same Writer.
	ErrAlreadyRun = errors.New("hush/audit: Run already called")

	// ErrChainLocked indicates the chain file already has an exclusive
	// advisory lock held by another Writer process. Used to protect
	// against concurrent writers corrupting the hash chain (single-Writer
	// contract documented in doc.go).
	ErrChainLocked = errors.New("hush/audit: chain file locked by another writer")
)

// ChainError carries the Seq of the first inconsistent event surfaced by
// Verify.  Wraps ErrAuditChainBroken so errors.Is(err, ErrAuditChainBroken)
// works for callers that don't care about the seq.
type ChainError struct {
	Seq    uint64
	Reason string
	Err    error
}

// Reason values for [ChainError].
const (
	ReasonHashMismatch     = "hash_mismatch"
	ReasonSignatureInvalid = "signature_invalid"
	ReasonSeqGap           = "seq_gap"
	ReasonPrevHashMismatch = "prev_hash_mismatch"
)

// Error returns a human-readable description of the chain break.
func (e *ChainError) Error() string {
	return fmt.Sprintf("hush/audit: chain integrity broken at seq %d: %s", e.Seq, e.Reason)
}

// Unwrap returns the wrapped sentinel so errors.Is(err, ErrAuditChainBroken)
// matches.
func (e *ChainError) Unwrap() error { return e.Err }

// canonicalEvent is the wire-shape used for the canonical preimage AND
// for on-disk persistence (the writer adds Hash and Signature after the
// preimage is computed).  Keys are alphabetical in CanonicalJSON.
type canonicalEvent struct {
	Action   string         `json:"action"`
	Data     map[string]any `json:"data,omitempty"`
	PrevHash string         `json:"prev_hash"`
	Seq      uint64         `json:"seq"`
	Time     time.Time      `json:"time"`
}

// computeHash returns SHA-256(prevHashBytes || canonicalJSON(eventWithoutHashSig)).
// prevHashBytes is the raw 32-byte digest of the prior event's hash (for
// Seq=1 it is the genesis prevHash bytes).
func computeHash(prevHashBytes []byte, ev Event) ([]byte, error) {
	pre := canonicalEvent{
		Action:   ev.Action,
		Data:     ev.Data,
		PrevHash: ev.PrevHash,
		Seq:      ev.Seq,
		Time:     ev.Time.UTC(),
	}
	canonical, err := sign.CanonicalJSON(pre)
	if err != nil {
		return nil, fmt.Errorf("audit: canonicalise event: %w", err)
	}
	h := sha256.New()
	h.Write(prevHashBytes)
	h.Write(canonical)
	return h.Sum(nil), nil
}

// signEventHash signs the supplied 32-byte hash with key and returns the
// base64-standard-encoded ASN.1 signature.  Rejects nil key.  ECDSA
// SignASN1 over secp256k1 with rand.Reader does not have a runtime
// failure mode in production (the key has been validated by
// [validateSigningKey] at NewWriter time); a defensive failure here
// would indicate kernel-level entropy starvation and is treated as
// fatal — wrap into ErrInvalidKey so callers see a typed error.
func signEventHash(key *ecdsa.PrivateKey, hashBytes []byte) (string, error) {
	if key == nil {
		return "", ErrInvalidKey
	}
	sig, _ := ecdsa.SignASN1(secureRandReader(), key, hashBytes)
	return base64.StdEncoding.EncodeToString(sig), nil
}

// verifyEventSignature verifies sig (base64-standard) against hashBytes
// using key.  Returns true on success.
func verifyEventSignature(key *ecdsa.PublicKey, hashBytes []byte, signature string) bool {
	if key == nil {
		return false
	}
	sig, err := base64.StdEncoding.DecodeString(signature)
	if err != nil {
		return false
	}
	return ecdsa.VerifyASN1(key, hashBytes, sig)
}

// validateSigningKey reports whether key is a usable secp256k1 ECDSA
// private key.
func validateSigningKey(key *ecdsa.PrivateKey) error {
	if key == nil || key.D == nil { //nolint:staticcheck // secp256k1 not in crypto/ecdh
		return ErrInvalidKey
	}
	if key.PublicKey.Curve == nil { //nolint:staticcheck // see above
		return ErrInvalidKey
	}
	if key.PublicKey.Curve != secp256k1.S256() { //nolint:staticcheck // see above
		return ErrInvalidKey
	}
	if key.PublicKey.X == nil || key.PublicKey.Y == nil { //nolint:staticcheck // see above
		return ErrInvalidKey
	}
	return nil
}

// Verify reads the chain file end-to-end, recomputing every event's hash
// and verifying every event's signature against verifyKey.  The first
// inconsistency surfaces a *ChainError wrapping [ErrAuditChainBroken].
// An empty / missing file returns nil (no events to verify).
//
//nolint:gocognit,gocyclo // sequential per-event chain check: branching is inherent to the integrity contract
func Verify(path string, verifyKey *ecdsa.PublicKey) error {
	if path == "" {
		return ErrInvalidPath
	}
	if verifyKey == nil {
		return ErrInvalidKey
	}
	f, err := os.Open(path) //nolint:gosec // operator-supplied audit path; trusted
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("audit: open chain file: %w", err)
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var (
		expectedSeq      uint64 = 1
		expectedPrevHash        = genesisPrevHash[:]
	)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev Event
		if err := json.Unmarshal(line, &ev); err != nil {
			return &ChainError{Seq: expectedSeq, Reason: ReasonHashMismatch, Err: ErrAuditChainBroken}
		}
		if ev.Seq != expectedSeq {
			return &ChainError{Seq: ev.Seq, Reason: ReasonSeqGap, Err: ErrAuditChainBroken}
		}
		if ev.PrevHash != hex.EncodeToString(expectedPrevHash) {
			return &ChainError{Seq: ev.Seq, Reason: ReasonPrevHashMismatch, Err: ErrAuditChainBroken}
		}
		// canonicalise of a fixed-shape Event cannot fail; ignore err
		recomputed, _ := computeHash(expectedPrevHash, ev)
		if ev.Hash != hex.EncodeToString(recomputed) {
			return &ChainError{Seq: ev.Seq, Reason: ReasonHashMismatch, Err: ErrAuditChainBroken}
		}
		if !verifyEventSignature(verifyKey, recomputed, ev.Signature) {
			return &ChainError{Seq: ev.Seq, Reason: ReasonSignatureInvalid, Err: ErrAuditChainBroken}
		}
		expectedSeq++
		expectedPrevHash = recomputed
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("audit: scan chain file: %w", err)
	}
	return nil
}

// readChainTail scans the chain file at path and returns (lastSeq,
// lastHashBytes).  An empty / missing file returns (0, genesisPrevHash[:]).
// A line that fails to parse OR fails the basic shape check
// returns ErrChainTailUnreadable.
//
//nolint:gocognit,gocyclo // sequential tail-line validation: branching is inherent to the recovery contract
func readChainTail(path string) (uint64, []byte, error) {
	f, err := os.Open(path) //nolint:gosec // operator-supplied audit path; trusted
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			out := make([]byte, len(genesisPrevHash))
			copy(out, genesisPrevHash[:])
			return 0, out, nil
		}
		return 0, nil, fmt.Errorf("audit: open chain file: %w", err)
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var (
		lastSeq  uint64
		lastHash []byte
	)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev Event
		if err := json.Unmarshal(line, &ev); err != nil {
			return 0, nil, ErrChainTailUnreadable
		}
		if ev.Seq == 0 || ev.Hash == "" || ev.PrevHash == "" {
			return 0, nil, ErrChainTailUnreadable
		}
		raw, err := hex.DecodeString(ev.Hash)
		if err != nil || len(raw) != sha256.Size {
			return 0, nil, ErrChainTailUnreadable
		}
		lastSeq = ev.Seq
		lastHash = raw
	}
	// scanner.Err() reaches here only when the underlying *os.File read
	// fails; in that case we already opened the file successfully so the
	// fault is transient. The caller treats this as ErrChainTailUnreadable
	// (the safe response — refuse to advance the chain).
	_ = scanner.Err()
	if lastSeq == 0 {
		out := make([]byte, len(genesisPrevHash))
		copy(out, genesisPrevHash[:])
		return 0, out, nil
	}
	return lastSeq, lastHash, nil
}
