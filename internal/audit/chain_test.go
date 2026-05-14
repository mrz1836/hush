package audit

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"

	"github.com/mrz1836/hush/internal/keys"
	"github.com/mrz1836/hush/internal/testutil"
	"github.com/mrz1836/hush/internal/transport/sign"
)

func jsonStdUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

// freshSecp256k1Key generates a new secp256k1 ECDSA key for tests that need
// a key distinct from the deterministic testutil seed.
func freshSecp256k1Key() (*ecdsa.PrivateKey, error) {
	priv, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		return nil, err
	}
	pub := priv.PubKey()
	return &ecdsa.PrivateKey{
		PublicKey: ecdsa.PublicKey{
			Curve: secp256k1.S256(), //nolint:staticcheck // secp256k1 not in crypto/ecdh
			X:     new(big.Int).SetBytes(pub.X().Bytes()[:]),
			Y:     new(big.Int).SetBytes(pub.Y().Bytes()[:]),
		},
		D: new(big.Int).SetBytes(priv.Serialize()),
	}, nil
}

// silence unused-import false positives if a build trims helpers below.
var _ = rand.Reader

func newTestSigningKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	seed := testutil.NewTestKeys(t)
	key, err := keys.DeriveAuditSigningKey(seed)
	if err != nil {
		t.Fatalf("DeriveAuditSigningKey: %v", err)
	}
	return key
}

func newTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// runWriter starts w.Run in a goroutine and returns a cancel func + a
// waiter the test calls to await Run's exit.
func runWriter(t *testing.T, w Writer) (cancel context.CancelFunc, wait func() error) {
	t.Helper()
	ctx, c := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- w.Run(ctx) }()
	return c, func() error {
		select {
		case err := <-errCh:
			return err
		case <-time.After(5 * time.Second):
			t.Fatal("writer.Run did not return within 5s of cancel")
			return nil
		}
	}
}

func appendN(t *testing.T, w Writer, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if err := w.Append(context.Background(), "test_event", map[string]any{"i": i}); err != nil {
			t.Fatalf("Append #%d: %v", i, err)
		}
	}
}

func readEvents(t *testing.T, path string) []Event {
	t.Helper()
	raw, err := os.ReadFile(path) //nolint:gosec // test path
	if err != nil {
		t.Fatalf("read chain: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	out := make([]Event, 0, len(lines))
	for _, line := range lines {
		if line == "" {
			continue
		}
		var ev Event
		if err := jsonUnmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		out = append(out, ev)
	}
	return out
}

// jsonUnmarshal is a tiny indirection so we can swap to a strict decoder
// later if needed.
func jsonUnmarshal(b []byte, v any) error {
	return jsonStdUnmarshal(b, v)
}

func TestAuditChain_GenesisPrevHashIsDomainSeparated(t *testing.T) {
	t.Parallel()
	expected := sha256.Sum256([]byte("hush.audit.chain.v1.genesis"))
	got := GenesisPrevHashForTest()
	if got != expected {
		t.Fatalf("genesisPrevHash mismatch")
	}
}

func TestAuditChain_HashLinkContiguous(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	key := newTestSigningKey(t)

	w, err := NewWriter(context.Background(), path, key, nil, newTestLogger())
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	cancel, wait := runWriter(t, w)
	appendN(t, w, 5)
	cancel()
	if err := wait(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	events := readEvents(t, path)
	if len(events) != 5 {
		t.Fatalf("got %d events; want 5", len(events))
	}
	genesis := GenesisPrevHashForTest()
	if events[0].PrevHash != hex.EncodeToString(genesis[:]) {
		t.Fatalf("first prev_hash mismatch: got %q", events[0].PrevHash)
	}
	for i, ev := range events {
		if ev.Seq != uint64(i+1) {
			t.Fatalf("event %d seq=%d; want %d", i, ev.Seq, i+1)
		}
		if i > 0 && ev.PrevHash != events[i-1].Hash {
			t.Fatalf("event %d prev_hash=%q; want %q", i, ev.PrevHash, events[i-1].Hash)
		}
	}
}

func TestAuditChain_SignatureValid(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	key := newTestSigningKey(t)

	w, err := NewWriter(context.Background(), path, key, nil, newTestLogger())
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	cancel, wait := runWriter(t, w)
	appendN(t, w, 3)
	cancel()
	if err := wait(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if err := Verify(path, &key.PublicKey); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func TestAuditChain_BreakDetectedOnTamper(t *testing.T) { //nolint:gocognit // sequential tamper-then-verify steps
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	key := newTestSigningKey(t)

	w, err := NewWriter(context.Background(), path, key, nil, newTestLogger())
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	cancel, wait := runWriter(t, w)
	for i := 0; i < 3; i++ {
		if err := w.Append(context.Background(), "approved", map[string]any{"i": i}); err != nil {
			t.Fatalf("Append #%d: %v", i, err)
		}
	}
	cancel()
	if err := wait(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Mutate event 2's data on disk: replace "approved" with "denied  "
	// (same byte length so the rest of the file remains aligned).
	raw, err := os.ReadFile(path) //nolint:gosec // test path
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines; got %d", len(lines))
	}
	mutated := strings.Replace(lines[1], `"action":"approved"`, `"action":"denied  "`, 1)
	if mutated == lines[1] {
		t.Fatalf("test setup: replace did not occur in line %q", lines[1])
	}
	lines[1] = mutated
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	err = Verify(path, &key.PublicKey)
	if !errors.Is(err, ErrAuditChainBroken) {
		t.Fatalf("Verify err = %v; want ErrAuditChainBroken", err)
	}
	var ce *ChainError
	if !errors.As(err, &ce) {
		t.Fatalf("errors.As ChainError: %v", err)
	}
	if ce.Seq != 2 {
		t.Fatalf("ce.Seq = %d; want 2 (first tampered event)", ce.Seq)
	}
	if ce.Reason != ReasonHashMismatch {
		t.Fatalf("ce.Reason = %q; want %q", ce.Reason, ReasonHashMismatch)
	}
}

func TestAuditChain_BreakDetectedOnDelete(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	key := newTestSigningKey(t)

	w, err := NewWriter(context.Background(), path, key, nil, newTestLogger())
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	cancel, wait := runWriter(t, w)
	appendN(t, w, 3)
	cancel()
	if err := wait(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	raw, err := os.ReadFile(path) //nolint:gosec // test path
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	// Delete the second line (Seq=2).
	survivors := []string{lines[0], lines[2]}
	if err := os.WriteFile(path, []byte(strings.Join(survivors, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	err = Verify(path, &key.PublicKey)
	if !errors.Is(err, ErrAuditChainBroken) {
		t.Fatalf("Verify err = %v; want ErrAuditChainBroken", err)
	}
	var ce *ChainError
	if !errors.As(err, &ce) {
		t.Fatalf("errors.As ChainError: %v", err)
	}
	// The deleted line means the third event's Seq=3 lands at expected
	// position 2, so we expect Seq=3 with seq_gap.
	if ce.Reason != ReasonSeqGap {
		t.Fatalf("ce.Reason = %q; want %q", ce.Reason, ReasonSeqGap)
	}
}

func TestAuditChain_BreakDetectedOnForgedSignature(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	key := newTestSigningKey(t)

	w, err := NewWriter(context.Background(), path, key, nil, newTestLogger())
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	cancel, wait := runWriter(t, w)
	appendN(t, w, 3)
	cancel()
	if err := wait(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	raw, err := os.ReadFile(path) //nolint:gosec // test path
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	// Replace event 2's signature field with a base64-valid-but-wrong one.
	// Use a freshly-generated key (NOT testutil's memoized seed, which would
	// collide with `key` and produce a still-valid signature).
	otherKey, err := freshSecp256k1Key()
	if err != nil {
		t.Fatalf("fresh key: %v", err)
	}
	hashBytes, err := computeHash(mustHexBytes(t, mustReadEvent(t, lines[0]).Hash), mustReadEvent(t, lines[1]))
	if err != nil {
		t.Fatalf("computeHash: %v", err)
	}
	wrongSig, err := signEventHash(otherKey, hashBytes)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	// Replace last "signature":"..." occurrence in line 1.
	mutated := replaceSignature(t, lines[1], wrongSig)
	lines[1] = mutated
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	err = Verify(path, &key.PublicKey)
	var ce *ChainError
	if !errors.As(err, &ce) {
		t.Fatalf("errors.As ChainError: %v (err=%v)", err, err)
	}
	if ce.Seq != 2 {
		t.Fatalf("ce.Seq = %d; want 2", ce.Seq)
	}
	if ce.Reason != ReasonSignatureInvalid {
		t.Fatalf("ce.Reason = %q; want %q", ce.Reason, ReasonSignatureInvalid)
	}
}

func TestAuditChain_HashCoversCanonicalEventWithoutHashOrSignature(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	key := newTestSigningKey(t)

	w, err := NewWriter(context.Background(), path, key, nil, newTestLogger())
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	cancel, wait := runWriter(t, w)
	if err := w.Append(context.Background(), "approved", map[string]any{"k": "v"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	cancel()
	if err := wait(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	events := readEvents(t, path)
	if len(events) != 1 {
		t.Fatalf("got %d events; want 1", len(events))
	}
	ev := events[0]

	// Recompute hash from canonical preimage using the public CanonicalJSON
	// path; this is the byte-level proof that Hash covers everything except
	// Hash and Signature.
	pre := canonicalEvent{
		Action:   ev.Action,
		Data:     ev.Data,
		PrevHash: ev.PrevHash,
		Seq:      ev.Seq,
		Time:     ev.Time.UTC(),
	}
	canon, err := sign.CanonicalJSON(pre)
	if err != nil {
		t.Fatalf("CanonicalJSON: %v", err)
	}
	prev, err := hex.DecodeString(ev.PrevHash)
	if err != nil {
		t.Fatalf("hex decode prev_hash: %v", err)
	}
	hash := sha256.Sum256(append(prev, canon...))
	if ev.Hash != hex.EncodeToString(hash[:]) {
		t.Fatalf("recomputed hash mismatch:\n got %s\n want %s", ev.Hash, hex.EncodeToString(hash[:]))
	}
}

// helpers

func mustReadEvent(t *testing.T, line string) Event {
	t.Helper()
	var ev Event
	if err := jsonStdUnmarshal([]byte(line), &ev); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return ev
}

func mustHexBytes(t *testing.T, h string) []byte {
	t.Helper()
	b, err := hex.DecodeString(h)
	if err != nil {
		t.Fatalf("hex decode: %v", err)
	}
	return b
}

// TestAuditChain_TornWriteRecovery simulates a crash mid-write (kernel
// panic / power loss between bufio.Flush and the next event) by
// truncating the chain file inside the last record. NewWriter MUST
// surface ErrChainTailUnreadable rather than silently appending from a
// corrupt prevHash, per the operator-intervention contract documented
// on ErrChainTailUnreadable.
func TestAuditChain_TornWriteRecovery(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	key := newTestSigningKey(t)

	// 1. Build a clean chain of 10 events.
	w, err := NewWriter(context.Background(), path, key, nil, newTestLogger())
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	cancel, wait := runWriter(t, w)
	appendN(t, w, 10)
	cancel()
	if err := wait(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// 2. Truncate the file inside the last line: read all bytes, find the
	//    second-to-last newline (boundary between events 9 and 10), then
	//    truncate to (boundary + half of event 10's body).
	raw, err := os.ReadFile(path) //nolint:gosec // test path
	if err != nil {
		t.Fatalf("read chain: %v", err)
	}
	lastNL := -1
	for i := len(raw) - 2; i >= 0; i-- { // -2 so the trailing \n on the last line is skipped
		if raw[i] == '\n' {
			lastNL = i
			break
		}
	}
	if lastNL < 0 {
		t.Fatalf("could not locate boundary newline; file=%dB", len(raw))
	}
	tailStart := lastNL + 1
	tailLen := len(raw) - tailStart
	if tailLen < 4 {
		t.Fatalf("last line too short to truncate: %d bytes", tailLen)
	}
	truncTo := tailStart + tailLen/2 // chop event 10 in half
	if err := os.WriteFile(path, raw[:truncTo], 0o600); err != nil {
		t.Fatalf("truncate chain: %v", err)
	}

	// 3. Reopen — NewWriter MUST refuse to append onto a torn tail.
	_, err = NewWriter(context.Background(), path, key, nil, newTestLogger())
	if !errors.Is(err, ErrChainTailUnreadable) {
		t.Fatalf("NewWriter on torn chain: got %v, want ErrChainTailUnreadable", err)
	}
}

// replaceSignature swaps the `"signature":"..."` field in a serialised
// event line with newSig. Returns the rewritten line. Fatally fails the
// test if the field is not present.
func replaceSignature(t *testing.T, line, newSig string) string {
	t.Helper()
	const tag = `"signature":"`
	i := strings.Index(line, tag)
	if i < 0 {
		t.Fatalf("signature field not found in %q", line)
	}
	j := strings.Index(line[i+len(tag):], `"`)
	if j < 0 {
		t.Fatalf("malformed signature field in %q", line)
	}
	return line[:i+len(tag)] + newSig + line[i+len(tag)+j:]
}

// extractFR14SupervisorScopeNames parses the §FR-14 section of SPEC.md and
// returns the set of supervisor_* / client_refresh_invoked tokens it lists.
func extractFR14SupervisorScopeNames(t *testing.T, specText string) map[string]struct{} {
	t.Helper()
	out := map[string]struct{}{}
	idx := strings.Index(specText, "### FR-14")
	if idx < 0 {
		t.Fatalf("FR-14 section not found in SPEC.md")
	}
	end := strings.Index(specText[idx:], "### FR-15")
	section := specText[idx:]
	if end > 0 {
		section = specText[idx : idx+end]
	}
	for _, tok := range strings.FieldsFunc(section, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\n' || r == '.' || r == '\t'
	}) {
		tok = strings.TrimSpace(tok)
		if strings.HasPrefix(tok, "supervisor_") || tok == "client_refresh_invoked" {
			out[tok] = struct{}{}
		}
	}
	return out
}

// TestSpecFR14AuditSync (SC-026-008) — mechanically asserts that the
// supervisor-scope audit-event names in docs/SPEC.md §FR-14 match the
// `supervisor_*` and `client_refresh_invoked` string values in
// internal/audit/chain.go's Action* constants 1:1. The test exists so
// that any future drift between the documented vocabulary and the
// emitted constants is caught at unit-test time, not in production.
func TestSpecFR14AuditSync(t *testing.T) {
	specPath := filepath.Join("..", "..", "docs", "SPEC.md")
	specBytes, err := os.ReadFile(specPath) //nolint:gosec // test path
	if err != nil {
		t.Fatalf("read SPEC.md: %v", err)
	}
	chainPath := filepath.Join("chain.go")
	chainBytes, err := os.ReadFile(chainPath) //nolint:gosec // test path
	if err != nil {
		t.Fatalf("read chain.go: %v", err)
	}

	// Parse the supervisor-scope subset out of §FR-14. The relevant
	// paragraph lists comma-separated event names; we extract every
	// supervisor_* / client_refresh_invoked token from the section.
	specSet := extractFR14SupervisorScopeNames(t, string(specBytes))

	// Parse the supervisor-scope subset from chain.go's constants block.
	// Each match is the right-hand string literal whose left-hand side is
	// an ActionSupervisor* or ActionClientRefresh* identifier.
	chainSet := map[string]struct{}{}
	for line := range strings.SplitSeq(string(chainBytes), "\n") {
		line = strings.TrimSpace(line)
		if !(strings.HasPrefix(line, "ActionSupervisor") || strings.HasPrefix(line, "ActionClientRefresh")) {
			continue
		}
		// Lines look like: `ActionFoo = "foo"` — extract the quoted value.
		q1 := strings.Index(line, `"`)
		q2 := strings.LastIndex(line, `"`)
		if q1 < 0 || q2 <= q1 {
			continue
		}
		chainSet[line[q1+1:q2]] = struct{}{}
	}

	// Both sets must be non-empty.
	if len(specSet) == 0 {
		t.Fatalf("no supervisor-scope names parsed from SPEC.md FR-14")
	}
	if len(chainSet) == 0 {
		t.Fatalf("no supervisor-scope constants parsed from chain.go")
	}

	// Assert identical sets.
	for name := range specSet {
		if _, ok := chainSet[name]; !ok {
			t.Errorf("FR-14 lists %q but chain.go has no matching constant", name)
		}
	}
	for name := range chainSet {
		if _, ok := specSet[name]; !ok {
			t.Errorf("chain.go declares %q but FR-14 does not list it", name)
		}
	}
}
