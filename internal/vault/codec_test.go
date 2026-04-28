package vault

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/mrz1836/hush/internal/vault/securebytes"
)

//nolint:gocognit // table-driven subtests; complexity is structural
func TestCodec_SealOpen_RoundTrip(t *testing.T) {
	t.Parallel()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	vk, err := securebytes.New(key)
	if err != nil {
		t.Fatalf("securebytes.New key: %v", err)
	}
	defer func() { _ = vk.Destroy() }()

	tests := []struct {
		name      string
		plaintext []byte
		nonce     []byte
	}{
		{"empty", []byte(`[]`), make([]byte, 12)},
		{"small", []byte(`[{"name":"x","description":"y","value":"aGVsbG8="}]`), make([]byte, 12)},
	}
	// Use a non-zero nonce for variety.
	tests[1].nonce[0] = 0x01

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			salt := make([]byte, saltLen)
			ct, err := aeadSeal(vk, salt, tc.nonce, tc.plaintext)
			if err != nil {
				t.Fatalf("seal: %v", err)
			}
			got, err := aeadOpen(vk, salt, tc.nonce, ct)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			if !bytes.Equal(got, tc.plaintext) {
				t.Fatalf("round-trip mismatch: want %q got %q", tc.plaintext, got)
			}
		})
	}
}

func TestCodec_WireValue_MarshalUnmarshal_NoStringAllocation(t *testing.T) {
	t.Parallel()

	originalData := "hunter2-secret-payload"
	original := []byte(originalData)
	sb, err := securebytes.New(original)
	if err != nil {
		t.Fatalf("securebytes.New: %v", err)
	}
	defer func() { _ = sb.Destroy() }()
	// original is now zeroed by securebytes.New; use originalData for comparisons.
	expected := []byte(originalData)

	// Marshal.
	wv := wireValue{sb: sb}
	data, err := json.Marshal(wv)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Verify the JSON form is a quoted base64 string.
	var s string
	if err = json.Unmarshal(data, &s); err != nil {
		t.Fatalf("outer unmarshal: %v", err)
	}
	decoded, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}
	if !bytes.Equal(decoded, expected) {
		t.Fatalf("base64 round-trip: want %q got %q", expected, decoded)
	}

	// Unmarshal into a new wireValue.
	var wv2 wireValue
	if err = json.Unmarshal(data, &wv2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if wv2.sb == nil {
		t.Fatal("unmarshal produced nil SecureBytes")
	}
	defer func() { _ = wv2.sb.Destroy() }()

	// The resulting *SecureBytes yields the original bytes under Use.
	var got []byte
	if err = wv2.sb.Use(func(b []byte) {
		got = make([]byte, len(b))
		copy(got, b)
	}); err != nil {
		t.Fatalf("Use: %v", err)
	}
	if !bytes.Equal(got, expected) {
		t.Fatalf("payload mismatch: want %q got %q", expected, got)
	}
}
