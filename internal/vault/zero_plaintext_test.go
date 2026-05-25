package vault

// Regression tests for the Layer 5 invariant that decryptWires (load path)
// and SaveWithSalt (save path) zero their aggregate plaintext buffers before
// returning, so a post-load / post-save heap snapshot cannot recover the
// JSON-encoded secret table that exists transiently in either flow.
//
// Approach: hook newAEAD to wrap the real GCM AEAD with an aeadCapture that
// retains (a) the original slice header passed through Seal / returned from
// Open and (b) a pre-zero copy of its contents. After the function under
// test returns, the captured slice (still pointing at the same backing
// array) must be all zeros, AND the pre-zero copy must contain the base64
// of a marker secret to prove the test actually exercised the path rather
// than observing an empty / unrelated buffer.

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/mrz1836/hush/internal/vault/securebytes"
)

// aeadCapture wraps a real AEAD and exposes the slices passing through
// Open and Seal so a test can assert post-return zeroing. Nil pointer
// fields disable the corresponding capture.
type aeadCapture struct {
	inner     cipher.AEAD
	openSlice *[]byte
	openCopy  *[]byte
	sealSlice *[]byte
	sealCopy  *[]byte
}

func (c *aeadCapture) NonceSize() int { return c.inner.NonceSize() }

func (c *aeadCapture) Overhead() int { return c.inner.Overhead() }

func (c *aeadCapture) Seal(dst, nonce, plaintext, ad []byte) []byte {
	if c.sealSlice != nil {
		*c.sealSlice = plaintext
	}
	if c.sealCopy != nil {
		snap := make([]byte, len(plaintext))
		copy(snap, plaintext)
		*c.sealCopy = snap
	}
	return c.inner.Seal(dst, nonce, plaintext, ad)
}

func (c *aeadCapture) Open(dst, nonce, ciphertext, ad []byte) ([]byte, error) {
	pt, err := c.inner.Open(dst, nonce, ciphertext, ad)
	if err != nil {
		return pt, err
	}
	if c.openSlice != nil {
		*c.openSlice = pt
	}
	if c.openCopy != nil {
		snap := make([]byte, len(pt))
		copy(snap, pt)
		*c.openCopy = snap
	}
	return pt, err
}

// installCaptureAEAD swaps newAEAD for a constructor that wraps the real
// GCM AEAD with an aeadCapture pointing at the supplied capture targets.
// Returns the restore func; caller is responsible for deferring it.
func installCaptureAEAD(t *testing.T, openSlice, openCopy, sealSlice, sealCopy *[]byte) func() {
	t.Helper()
	return SetNewAEAD(func(k []byte) (cipher.AEAD, error) {
		block, err := aes.NewCipher(k)
		if err != nil {
			return nil, err
		}
		inner, err := cipher.NewGCM(block)
		if err != nil {
			return nil, err
		}
		return &aeadCapture{
			inner:     inner,
			openSlice: openSlice,
			openCopy:  openCopy,
			sealSlice: sealSlice,
			sealCopy:  sealCopy,
		}, nil
	})
}

// assertPlaintextZeroed checks the Layer-5 zeroing invariant captured by an
// aeadCapture: snapshot must contain the base64 marker (proves the capture
// hook fired against a real plaintext) and slice (aliasing the same backing
// array) must be fully zero (proves the function under test ran the
// defer secureZero before returning). opName tags failures by call site.
func assertPlaintextZeroed(t *testing.T, opName string, marker, snapshot, slice []byte) {
	t.Helper()
	if len(snapshot) == 0 {
		t.Fatalf("%s: test setup: snapshot empty — capture hook did not run", opName)
	}
	markerB64 := base64.StdEncoding.EncodeToString(marker)
	if !bytes.Contains(snapshot, []byte(markerB64)) {
		t.Fatalf("%s: test setup: pre-zero snapshot missing marker base64; got: %q", opName, snapshot)
	}
	if len(slice) != len(snapshot) {
		t.Fatalf("%s: slice / snapshot length mismatch: %d vs %d", opName, len(slice), len(snapshot))
	}
	for i, b := range slice {
		if b != 0 {
			t.Fatalf("%s: did not zero plaintext: byte %d = 0x%02x (slice len %d)", opName, i, b, len(slice))
		}
	}
}

// buildSealedEnvelope marshals a single-secret wire payload, seals it with
// the supplied vault key, and returns the on-wire HUSH envelope. Used by
// the decrypt-zero regression test to produce a valid input WITHOUT the
// aeadCapture hook installed, so only the subsequent Open is observed.
func buildSealedEnvelope(t *testing.T, vk *securebytes.SecureBytes, marker []byte) []byte {
	t.Helper()
	sb, err := securebytes.New(append([]byte(nil), marker...))
	if err != nil {
		t.Fatalf("securebytes.New: %v", err)
	}
	t.Cleanup(func() { _ = sb.Destroy() })

	wires := []wireSecret{{Name: "x", Description: "d", Value: wireValue{sb: sb}}}
	plaintext, err := json.Marshal(wires)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	salt := make([]byte, saltLen)
	nonce := make([]byte, nonceLen)
	nonce[0] = 0x42
	ct, err := aeadSeal(vk, salt, nonce, plaintext)
	if err != nil {
		t.Fatalf("aeadSeal: %v", err)
	}
	env := make([]byte, 0, headerLen+len(ct))
	env = append(env, magic...)
	env = append(env, version)
	env = append(env, salt...)
	env = append(env, nonce...)
	env = append(env, ct...)
	return env
}

// TestDecryptWires_ZeroesPlaintextAfterReturn pins the Layer 5 invariant
// that decryptWires zeros its aeadOpen plaintext buffer before returning.
// Regression guard for the C1 finding in the crypto-surface audit: the
// decrypted JSON aggregate (containing base64 of every secret value) must
// not float in unreferenced heap memory after the function returns.
func TestDecryptWires_ZeroesPlaintextAfterReturn(t *testing.T) {
	// Uses SetNewAEAD global hook — do NOT call t.Parallel().
	vk := makeVaultKey(t, 0xC1)
	marker := []byte("HUSH_C1_DECRYPT_REGRESSION_MARKER_xyz")
	envelope := buildSealedEnvelope(t, vk, marker)

	var openSlice, openCopy []byte
	restore := installCaptureAEAD(t, &openSlice, &openCopy, nil, nil)
	defer restore()

	out, err := decryptWires(envelope, vk)
	if err != nil {
		t.Fatalf("decryptWires: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("want 1 wire, got %d", len(out))
	}
	if out[0].Value.sb != nil {
		_ = out[0].Value.sb.Destroy()
	}

	assertPlaintextZeroed(t, "decryptWires", marker, openCopy, openSlice)
}

// TestSaveWithSalt_ZeroesPlaintextAfterReturn pins the Layer 5 invariant
// that SaveWithSalt zeros its json.Marshal aggregate before returning.
// Regression guard for the save-path analog of the C1 finding: the
// JSON-encoded secret table fed to aeadSeal must not float in unreferenced
// heap memory after the function returns.
func TestSaveWithSalt_ZeroesPlaintextAfterReturn(t *testing.T) {
	// Uses SetNewAEAD global hook — do NOT call t.Parallel().
	dir := makeTestDir(t)
	path := filepath.Join(dir, "vault.hush")
	vk := makeVaultKey(t, 0xC2)

	marker := []byte("HUSH_C2_SAVE_REGRESSION_MARKER_xyz")
	s := makeSecret(t, "KEY", "desc", append([]byte(nil), marker...))

	salt := make([]byte, saltLen)
	var sealSlice, sealCopy []byte
	restore := installCaptureAEAD(t, nil, nil, &sealSlice, &sealCopy)
	defer restore()

	if err := SaveWithSalt(context.Background(), path, vk, salt, []Secret{s}); err != nil {
		t.Fatalf("SaveWithSalt: %v", err)
	}

	assertPlaintextZeroed(t, "SaveWithSalt", marker, sealCopy, sealSlice)
}

// TestSecureZero pins the trivial helper. A future refactor that swaps
// the simple loop for a stdlib call must preserve the per-byte-zero
// guarantee that the defer registrations in file.go rely on.
func TestSecureZero(t *testing.T) {
	t.Parallel()
	buf := []byte{0x11, 0x22, 0x33, 0x44, 0x55}
	secureZero(buf)
	for i, b := range buf {
		if b != 0 {
			t.Fatalf("secureZero: byte %d = 0x%02x, want 0x00", i, b)
		}
	}
	// Empty buffer is a no-op (does not panic).
	secureZero(nil)
	secureZero([]byte{})
}
