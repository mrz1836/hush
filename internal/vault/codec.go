package vault

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/mrz1836/hush/internal/vault/securebytes"
)

var errNotJSONString = errors.New("vault: value is not a JSON string")

// newAEAD combines aes.NewCipher + cipher.NewGCM into a single call.
// Replaceable in tests to cover error paths.
//
//nolint:gochecknoglobals // AEAD bridge; test-hookable for error-path coverage
var newAEAD = func(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// sbNewFromUnmarshal is the securebytes.New bridge used by wireValue.UnmarshalJSON.
// Replaceable in tests to cover mlock-failure paths.
//
//nolint:gochecknoglobals // securebytes bridge; test-hookable for mlock-failure path coverage
var sbNewFromUnmarshal = securebytes.New

// wireSecret is the on-the-wire shape of one vault entry (inside the AEAD envelope).
type wireSecret struct {
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Value       wireValue `json:"value"`
}

// wireValue holds the secret payload via a SecureBytes pointer.
// Custom (Un)MarshalJSON bypasses any Go-string allocation of the raw secret value.
type wireValue struct {
	sb *securebytes.SecureBytes
}

// MarshalJSON base64-encodes the payload from inside the SecureBytes borrow.
// No Go string is allocated for the secret value.
func (w wireValue) MarshalJSON() ([]byte, error) {
	var result []byte
	err := w.sb.Use(func(b []byte) {
		enc := base64.StdEncoding.EncodeToString(b)
		result, _ = json.Marshal(enc)
	})
	if err != nil {
		return nil, fmt.Errorf("vault: marshal value: %w", err)
	}
	return result, nil
}

// UnmarshalJSON decodes a base64-quoted JSON string directly into a SecureBytes,
// never materializing the secret value as a Go string.
func (w *wireValue) UnmarshalJSON(data []byte) error {
	// data must be a JSON-quoted string: "..."
	if len(data) < 2 || data[0] != '"' || data[len(data)-1] != '"' {
		return errNotJSONString
	}
	// Strip the surrounding quotes to get the bare base64 bytes.
	b64 := data[1 : len(data)-1]

	buf := make([]byte, base64.StdEncoding.DecodedLen(len(b64)))
	n, err := base64.StdEncoding.Decode(buf, b64)
	if err != nil {
		return fmt.Errorf("vault: base64 decode: %w", err)
	}
	buf = buf[:n]

	// sbNewFromUnmarshal copies buf into mlocked memory and zeroes buf.
	sb, err := sbNewFromUnmarshal(buf)
	if err != nil {
		return fmt.Errorf("vault: securebytes.New: %w", err)
	}
	w.sb = sb
	return nil
}

// aeadSeal encrypts plaintext with AES-256-GCM.
// The 32-byte key is borrowed from vaultKey only for the duration of the seal.
func aeadSeal(vaultKey *securebytes.SecureBytes, salt, nonce, plaintext []byte) ([]byte, error) {
	_ = salt // carried; not used as key material here (KDF is upstream)
	var (
		ciphertext []byte
		sealErr    error
	)
	err := vaultKey.Use(func(key []byte) {
		gcm, e := newAEAD(key)
		if e != nil {
			sealErr = e
			return
		}
		ciphertext = gcm.Seal(nil, nonce, plaintext, nil)
	})
	if err != nil {
		return nil, fmt.Errorf("vault: seal key borrow: %w", err)
	}
	if sealErr != nil {
		return nil, fmt.Errorf("vault: seal: %w", sealErr)
	}
	return ciphertext, nil
}

// aeadOpen decrypts ciphertext with AES-256-GCM.
// The 32-byte key is borrowed from vaultKey only for the duration of the open.
func aeadOpen(vaultKey *securebytes.SecureBytes, salt, nonce, ciphertext []byte) ([]byte, error) {
	_ = salt
	var (
		plaintext []byte
		openErr   error
	)
	err := vaultKey.Use(func(key []byte) {
		gcm, e := newAEAD(key)
		if e != nil {
			openErr = e
			return
		}
		plaintext, openErr = gcm.Open(nil, nonce, ciphertext, nil)
	})
	if err != nil {
		return nil, fmt.Errorf("vault: open key borrow: %w", err)
	}
	if openErr != nil {
		return nil, openErr
	}
	return plaintext, nil
}
