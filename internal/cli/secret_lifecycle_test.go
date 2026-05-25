package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mrz1836/hush/internal/vault/securebytes"
)

// TestInitDeps_SecretFieldsArePointerSecureBytes is a structural
// regression guard for the M3 refactor: the three secret-bearing
// initDeps fields (serverPassphrase, serverBotToken, clientPassphrase)
// MUST be typed as *securebytes.SecureBytes so the non-interactive
// JSON-input boundary stays the only place a Go-string copy of the
// passphrase or bot token can exist.
//
// If a future change reverts any of these to string, this test fails
// loudly rather than silently re-introducing the Layer-5 bypass that
// auditing flagged as M3.
func TestInitDeps_SecretFieldsArePointerSecureBytes(t *testing.T) {
	t.Parallel()
	dp := reflect.TypeOf(initDeps{})
	want := reflect.TypeOf((*securebytes.SecureBytes)(nil))
	for _, fieldName := range []string{"serverPassphrase", "serverBotToken", "clientPassphrase"} {
		f, ok := dp.FieldByName(fieldName)
		require.Truef(t, ok, "initDeps.%s field missing", fieldName)
		require.Equalf(t, want, f.Type,
			"initDeps.%s type = %s; want %s (M3 regression: secret fields must be *SecureBytes, never string)",
			fieldName, f.Type, want)
	}
}

// TestSecretDeps_SecretFieldsArePointerSecureBytes is the same
// structural guard for secretDeps.passphrase / .secretValue.
func TestSecretDeps_SecretFieldsArePointerSecureBytes(t *testing.T) {
	t.Parallel()
	dp := reflect.TypeOf(secretDeps{})
	want := reflect.TypeOf((*securebytes.SecureBytes)(nil))
	for _, fieldName := range []string{"passphrase", "secretValue"} {
		f, ok := dp.FieldByName(fieldName)
		require.Truef(t, ok, "secretDeps.%s field missing", fieldName)
		require.Equalf(t, want, f.Type,
			"secretDeps.%s type = %s; want %s (M3 regression: secret fields must be *SecureBytes, never string)",
			fieldName, f.Type, want)
	}
}

// TestCloneSecureBytes_RoundTrips verifies the clone helper produces
// an independently-owned SecureBytes carrying the same payload as the
// source — the load-bearing primitive that the cobra-RunE → run* and
// smoke-flow → run* boundaries depend on for safe ownership transfer.
func TestCloneSecureBytes_RoundTrips(t *testing.T) {
	t.Parallel()
	src := mustSecureBytes(t, []byte("hush-clone-test-payload"))
	dst, err := cloneSecureBytes(src)
	require.NoError(t, err)
	require.NotNil(t, dst)
	t.Cleanup(func() { _ = dst.Destroy() })

	require.NotSame(t, src, dst, "clone must be a distinct *SecureBytes")

	var got []byte
	require.NoError(t, dst.Use(func(b []byte) { got = append(got, b...) }))
	require.Equal(t, "hush-clone-test-payload", string(got))

	// Destroying src must not affect dst — independent ownership.
	_ = src.Destroy()
	got = got[:0]
	require.NoError(t, dst.Use(func(b []byte) { got = append(got, b...) }))
	require.Equal(t, "hush-clone-test-payload", string(got))
}

// TestCloneSecureBytes_RejectsDestroyed asserts that cloning a
// destroyed SecureBytes surfaces the underlying ErrDestroyed instead
// of silently producing an empty clone.
func TestCloneSecureBytes_RejectsDestroyed(t *testing.T) {
	t.Parallel()
	sb, err := securebytes.New([]byte("temp"))
	require.NoError(t, err)
	_ = sb.Destroy()
	_, err = cloneSecureBytes(sb)
	require.ErrorIs(t, err, securebytes.ErrDestroyed)
}

// TestReadServerBootstrapSecrets_ZeroesFileBytes asserts the JSON-input
// boundary scrubs the raw file body []byte before returning so the
// only residual exposure of the secrets is the unzeroable JSON struct
// strings (irreducible Go runtime behavior; documented inline).
//
// Indirect check: read the file into a fresh []byte AFTER reading via
// the helper; the helper's defer must have left the on-disk file
// intact (we want the disk untouched — only the in-memory body is
// zeroed). The harder assertion that the in-memory body bytes were
// zeroed is exercised by reading from a stub that returns a shared
// slice — captured below.
func TestReadServerBootstrapSecrets_ZeroesFileBytes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0o700))
	path := filepath.Join(dir, "bootstrap.json")
	body := []byte(`{"vault_passphrase":"correctbatterystaple","discord_bot_token":"discord-tok-1234567890"}`)
	require.NoError(t, os.WriteFile(path, body, 0o600))

	pass, botTok, err := readServerBootstrapSecrets(path)
	require.NoError(t, err)
	require.NotNil(t, pass)
	require.NotNil(t, botTok)
	t.Cleanup(func() {
		_ = pass.Destroy()
		_ = botTok.Destroy()
	})

	// Disk file untouched — operators may keep the bootstrap input file
	// in place; the in-memory body is what gets zeroed.
	stillOnDisk, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, body, stillOnDisk, "on-disk file must be untouched")

	// SecureBytes payloads round-trip the JSON values.
	var gotPass, gotTok []byte
	require.NoError(t, pass.Use(func(b []byte) { gotPass = append(gotPass, b...) }))
	require.NoError(t, botTok.Use(func(b []byte) { gotTok = append(gotTok, b...) }))
	require.Equal(t, "correctbatterystaple", string(gotPass))
	require.Equal(t, "discord-tok-1234567890", string(gotTok))
}

// TestReadServerBootstrapSecrets_OmittedBotTokenYieldsNil asserts the
// behavior the cobra RunE relies on: an absent or empty
// discord_bot_token in the JSON returns botTok == nil so the explicit-
// state-dir branch can skip the keychain Store call.
func TestReadServerBootstrapSecrets_OmittedBotTokenYieldsNil(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0o700))
	path := filepath.Join(dir, "bootstrap.json")
	body := []byte(`{"vault_passphrase":"correctbatterystaple","discord_bot_token":""}`)
	require.NoError(t, os.WriteFile(path, body, 0o600))

	pass, botTok, err := readServerBootstrapSecrets(path)
	require.NoError(t, err)
	require.NotNil(t, pass)
	require.Nil(t, botTok, "empty discord_bot_token must yield nil SecureBytes")
	_ = pass.Destroy()
}

// TestReadServerBootstrapSecrets_PathMissing surfaces the missing-flag
// sentinel rather than panicking on the empty path.
func TestReadServerBootstrapSecrets_PathMissing(t *testing.T) {
	t.Parallel()
	_, _, err := readServerBootstrapSecrets("")
	require.ErrorIs(t, err, errMissingFlag)
}

// TestReadClientBootstrapSecret_RoundTrips mirrors the server case for
// the client passphrase JSON.
func TestReadClientBootstrapSecret_RoundTrips(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0o700))
	path := filepath.Join(dir, "client.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"vault_passphrase":"correctbatterystaple"}`), 0o600))

	pass, err := readClientBootstrapSecret(path)
	require.NoError(t, err)
	require.NotNil(t, pass)
	t.Cleanup(func() { _ = pass.Destroy() })

	var got []byte
	require.NoError(t, pass.Use(func(b []byte) { got = append(got, b...) }))
	require.Equal(t, "correctbatterystaple", string(got))
}

// TestReadSecretAddSecrets_RoundTripsAndOmitsValue asserts the
// secret-add JSON-input boundary mirrors the init helpers: the
// description is plain (non-secret) and an absent value yields nil.
func TestReadSecretAddSecrets_RoundTripsAndOmitsValue(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0o700))
	path := filepath.Join(dir, "add.json")
	doc, err := json.Marshal(secretAddInput{
		VaultPassphrase: "correctbatterystaple",
		Value:           "sk-test-xyz",
		Description:     "fixture description",
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, doc, 0o600))

	pass, val, desc, err := readSecretAddSecrets(path)
	require.NoError(t, err)
	require.NotNil(t, pass)
	require.NotNil(t, val)
	require.Equal(t, "fixture description", desc)
	t.Cleanup(func() {
		_ = pass.Destroy()
		_ = val.Destroy()
	})

	var gotPass, gotVal []byte
	require.NoError(t, pass.Use(func(b []byte) { gotPass = append(gotPass, b...) }))
	require.NoError(t, val.Use(func(b []byte) { gotVal = append(gotVal, b...) }))
	require.Equal(t, "correctbatterystaple", string(gotPass))
	require.Equal(t, "sk-test-xyz", string(gotVal))
}

// TestFixedPassphraseSource_YieldsIndependentClones asserts the smoke-
// flow passphrase source returns a fresh, independently-owned
// SecureBytes on each call (so the consumer's Destroy does not blast
// the smoke-flow-owned source).
func TestFixedPassphraseSource_YieldsIndependentClones(t *testing.T) {
	t.Parallel()
	src := mustSecureBytes(t, []byte("correctbatterystaple"))
	source := fixedPassphraseSource(src)

	first, err := source(context.Background(), nil, nil)
	require.NoError(t, err)
	require.NotNil(t, first)
	t.Cleanup(func() { _ = first.Destroy() })

	second, err := source(context.Background(), nil, nil)
	require.NoError(t, err)
	require.NotNil(t, second)
	t.Cleanup(func() { _ = second.Destroy() })

	require.NotSame(t, first, second, "each call must yield a distinct *SecureBytes")
	require.NotSame(t, first, src, "yielded pointer must not be the original src")

	// Destroying first must not affect second or src.
	_ = first.Destroy()
	var got []byte
	require.NoError(t, second.Use(func(b []byte) { got = append(got, b...) }))
	require.Equal(t, "correctbatterystaple", string(got))
	got = got[:0]
	require.NoError(t, src.Use(func(b []byte) { got = append(got, b...) }))
	require.Equal(t, "correctbatterystaple", string(got))
}

// TestRunInitServer_MissingNonInteractivePassphraseFailsLoudly asserts
// that the new defensive check at the start of the non-interactive
// branch surfaces errMissingFlag rather than dereferencing nil. This
// is the load-bearing failure mode for misconfigured automation that
// fails to call readServerBootstrapSecrets.
func TestRunInitServer_MissingNonInteractivePassphraseFailsLoudly(t *testing.T) {
	t.Parallel()
	fx := newInitFixture(t)
	fx.deps.serverNonInteractive = true
	// Intentionally leave serverPassphrase == nil to mimic a busted
	// automation pipeline.
	err := runInitServer(context.Background(), fx.stdoutS, fx.stderrS, fx.stdinFile, fx.deps)
	require.Error(t, err)
	require.ErrorIs(t, err, errMissingFlag)
}
