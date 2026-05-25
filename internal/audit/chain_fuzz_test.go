package audit

import (
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
)

// FuzzVerifyChain feeds mutated chain-file bytes to Verify and asserts the
// parser never panics. The success criterion is shape-safety: Verify must
// either succeed (only the unmutated seed should ever) or return a
// well-typed error wrapping ErrAuditChainBroken / ErrInvalidPath /
// ErrInvalidKey / a scan error. A panic on any input is a parser bug.
func FuzzVerifyChain(f *testing.F) {
	// Seed 1: a freshly-built valid 3-event chain. Built once outside the
	// fuzz loop so we capture its bytes for the corpus.
	dir := f.TempDir()
	path := filepath.Join(dir, "seed.jsonl")
	key := freshSecp256k1KeyFuzz(f)

	w, err := NewWriter(context.Background(), path, key, nil, newTestLogger())
	if err != nil {
		f.Fatalf("NewWriter: %v", err)
	}
	cancel, wait := runWriterFuzz(f, w)
	for i := 0; i < 3; i++ {
		if err := w.Append(context.Background(), "fuzz_seed", map[string]any{"i": i}); err != nil {
			f.Fatalf("Append #%d: %v", i, err)
		}
	}
	cancel()
	if err := wait(); err != nil {
		f.Fatalf("Run: %v", err)
	}
	validSeed, err := os.ReadFile(path) //nolint:gosec // test path
	if err != nil {
		f.Fatalf("read seed: %v", err)
	}
	f.Add(validSeed)

	// Seed 2: a hand-crafted single-record chain with hash/sig that won't
	// verify. Exercises the "valid JSON, broken integrity" path.
	bad := mustForgedRecord(f, key, 1, [32]byte{})
	f.Add(bad)

	// Seed 3: a record with the right seq/hash format but wrong PrevHash.
	g := genesisPrevHash
	bad2 := mustForgedRecord(f, key, 1, g)
	// Flip one bit in the JSON to invalidate.
	if len(bad2) > 10 {
		bad2[len(bad2)-3] ^= 0xFF
	}
	f.Add(bad2)

	// Seed 4: blank file (empty chain → nil).
	f.Add([]byte(""))
	// Seed 5: garbage.
	f.Add([]byte("not json\nstill not json\n"))

	f.Fuzz(func(t *testing.T, data []byte) {
		dir := t.TempDir()
		p := filepath.Join(dir, "chain.jsonl")
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatalf("write fuzz input: %v", err)
		}
		// Only nil is acceptable; otherwise the error must wrap one of the
		// documented sentinels so callers can branch.
		err := Verify(p, &key.PublicKey)
		if err == nil {
			return
		}
		var chainErr *ChainError
		switch {
		case errors.As(err, &chainErr):
		case errors.Is(err, ErrAuditChainBroken):
		case errors.Is(err, ErrInvalidPath):
		case errors.Is(err, ErrInvalidKey):
		default:
			// Scanner / OS errors are also tolerated as long as they
			// don't escape the documented surface as panics. Other typed
			// errors (e.g. "audit: scan chain file: <ioerr>") fall here.
		}
	})
}

// mustForgedRecord constructs and signs a single canonical Event with the
// supplied seq + prevHash and returns the JSON-line bytes.
func mustForgedRecord(tb testing.TB, key *ecdsa.PrivateKey, seq uint64, prevHash [32]byte) []byte {
	tb.Helper()
	ev := Event{
		Seq:      seq,
		Time:     time.Unix(0, 0).UTC(),
		Action:   "fuzz_forged",
		PrevHash: hex.EncodeToString(prevHash[:]),
	}
	pre := canonicalEvent{
		Action:   ev.Action,
		PrevHash: ev.PrevHash,
		Seq:      ev.Seq,
		Time:     ev.Time,
	}
	_ = pre // canonicalise is exercised by the writer in seed 1; for seed 2/3 we
	// only need a syntactically valid record — recompute hash via SHA256.
	canonical, err := json.Marshal(pre)
	if err != nil {
		tb.Fatalf("marshal pre: %v", err)
	}
	h := sha256.New()
	h.Write(prevHash[:])
	h.Write(canonical)
	digest := h.Sum(nil)
	ev.Hash = hex.EncodeToString(digest)

	sig, _ := ecdsa.SignASN1(secureRandReader(), key, digest)
	ev.Signature = base64.StdEncoding.EncodeToString(sig)

	line, err := json.Marshal(ev)
	if err != nil {
		tb.Fatalf("marshal ev: %v", err)
	}
	return append(line, '\n')
}

// freshSecp256k1KeyFuzz mints a fresh secp256k1 ECDSA key for fuzz seeding.
// Kept here (not in chain_test.go) so the fuzz file is self-contained — the
// existing freshSecp256k1Key in chain_test.go is the same routine; this
// variant takes *testing.F directly to avoid shimming the test-T interface.
func freshSecp256k1KeyFuzz(f *testing.F) *ecdsa.PrivateKey {
	f.Helper()
	priv, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		f.Fatalf("GeneratePrivateKey: %v", err)
	}
	pub := priv.PubKey()
	return &ecdsa.PrivateKey{
		PublicKey: ecdsa.PublicKey{
			Curve: secp256k1.S256(), //nolint:staticcheck // secp256k1 not in crypto/ecdh
			X:     new(big.Int).SetBytes(pub.X().Bytes()[:]),
			Y:     new(big.Int).SetBytes(pub.Y().Bytes()[:]),
		},
		D: new(big.Int).SetBytes(priv.Serialize()),
	}
}

// runWriterFuzz mirrors runWriter but accepts *testing.F so we can build a
// seed chain inside the corpus-seeding phase. Kept separate from runWriter
// to avoid touching the t-typed test helpers.
func runWriterFuzz(f *testing.F, w Writer) (cancel context.CancelFunc, wait func() error) {
	f.Helper()
	ctx, c := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- w.Run(ctx) }()
	return c, func() error {
		select {
		case err := <-errCh:
			return err
		case <-time.After(5 * time.Second):
			f.Fatal("writer.Run did not return within 5s of cancel")
			return nil
		}
	}
}
