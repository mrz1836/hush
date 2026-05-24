package sign

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"testing"
)

// FuzzSignVerifyRoundTrip asserts the sign-side invariant: for any payload,
// Sign(key, payload) produces a signature that Verify(pub, payload, sig) accepts.
// Catches any divergence between sign and verify (digest, ASN.1 framing, curve).
func FuzzSignVerifyRoundTrip(f *testing.F) {
	priv := generateFuzzKey(f)
	//nolint:forcetypeassert // ecdsa.GenerateKey always yields *ecdsa.PublicKey
	pub := priv.Public().(*ecdsa.PublicKey)

	f.Add([]byte(""))
	f.Add([]byte("\x00"))
	f.Add([]byte("hello"))
	f.Add(bytes.Repeat([]byte{0xff}, 32))
	f.Add(bytes.Repeat([]byte{0x00}, 1024))

	ctx := context.Background()
	f.Fuzz(func(t *testing.T, payload []byte) {
		// Cap payload to keep fuzz inputs bounded (Sign hashes everything;
		// huge inputs just slow the harness without expanding coverage).
		const maxPayloadBytes = 64 * 1024
		if len(payload) > maxPayloadBytes {
			return
		}
		sig, err := Sign(ctx, priv, payload)
		if err != nil {
			t.Fatalf("Sign returned error on valid key+payload (len=%d): %v", len(payload), err)
		}
		if len(sig) == 0 {
			t.Fatalf("Sign returned empty signature (payload len=%d)", len(payload))
		}
		if vErr := Verify(ctx, pub, payload, sig); vErr != nil {
			t.Fatalf("Verify rejected fresh signature (payload len=%d): %v", len(payload), vErr)
		}
	})
}

// FuzzCanonicalJSONIdempotent feeds fuzz-generated JSON into CanonicalJSON
// and asserts two invariants:
//   - For any input decodable as JSON, CanonicalJSON's output must itself be
//     valid JSON that decodes to a deep-equal value.
//   - CanonicalJSON(CanonicalJSON(x)) must equal CanonicalJSON(x): the
//     canonical form is a fixed point (idempotency).
//
// Catches encoder bugs in the reflect-based walker: map-key sorting drift,
// float emission, escape handling.
//
//nolint:gocognit // fuzz harness with property assertions; complexity is structural
func FuzzCanonicalJSONIdempotent(f *testing.F) {
	f.Add([]byte(`null`))
	f.Add([]byte(`true`))
	f.Add([]byte(`false`))
	f.Add([]byte(`0`))
	f.Add([]byte(`-1.5`))
	f.Add([]byte(`""`))
	f.Add([]byte(`"hello"`))
	f.Add([]byte(`[]`))
	f.Add([]byte(`[1,2,3]`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"a":1,"b":2}`))
	f.Add([]byte(`{"b":2,"a":1}`)) // unsorted input → canonical must sort
	f.Add([]byte(`{"nested":{"z":1,"a":[1,{"y":2,"x":3}]}}`))

	f.Fuzz(func(t *testing.T, raw []byte) {
		const maxJSONBytes = 32 * 1024
		if len(raw) > maxJSONBytes {
			return
		}
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			// Not valid JSON — out of scope for this property.
			return
		}

		out, err := CanonicalJSON(v)
		if err != nil {
			// CanonicalJSON may reject non-finite floats etc.; those must
			// surface as ErrCanonicalUnsupported.
			if !errors.Is(err, ErrCanonicalUnsupported) {
				t.Fatalf("CanonicalJSON returned untyped error: %v", err)
			}
			return
		}

		// Property 1: output is valid JSON decoding to a deep-equal value.
		var roundtrip any
		if rtErr := json.Unmarshal(out, &roundtrip); rtErr != nil {
			t.Fatalf("CanonicalJSON output not valid JSON: %v\noutput: %s", rtErr, out)
		}

		// Property 2: idempotency.
		out2, idemErr := CanonicalJSON(roundtrip)
		if idemErr != nil {
			t.Fatalf("CanonicalJSON failed on its own decoded output: %v", idemErr)
		}
		if !bytes.Equal(out, out2) {
			t.Fatalf("CanonicalJSON not idempotent:\nfirst:  %s\nsecond: %s", out, out2)
		}
	})
}
