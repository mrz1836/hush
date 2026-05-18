//go:build darwin

package keychain

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mrz1836/hush/internal/vault/securebytes"
)

// fakeExitErr returns an *exec.ExitError-compatible value with the
// supplied exit code. Constructed by running a tiny no-op binary
// that exits with the requested code.
func fakeExitErr(t *testing.T, code int) error {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "sh", "-c", "exit "+itoa(code))
	err := cmd.Run()
	if code == 0 {
		require.NoError(t, err)
		return nil
	}
	var ee *exec.ExitError
	require.True(t, errors.As(err, &ee))
	require.Equal(t, code, ee.ExitCode())
	return err
}

// itoa is a tiny stdlib-free integer-to-string helper.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	buf := make([]byte, 0, 4)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		buf = append([]byte{'0' + byte(n%10)}, buf...)
		n /= 10
	}
	if neg {
		buf = append([]byte{'-'}, buf...)
	}
	return string(buf)
}

func newDarwinForTest() *darwinKeychain {
	return &darwinKeychain{
		logger:   slog.Default(),
		binary:   "/usr/bin/security",
		runFn:    func(*exec.Cmd) error { return nil },
		outputFn: func(*exec.Cmd) ([]byte, error) { return nil, nil },
	}
}

func TestKeychainDarwin_ConstructedSecurityCommand(t *testing.T) {
	t.Parallel()
	k := newDarwinForTest()

	var captured []string
	var stdinBytes []byte
	k.runFn = func(cmd *exec.Cmd) error {
		captured = append([]string(nil), cmd.Args...)
		require.Equal(t, "/usr/bin/security", cmd.Path)
		if cmd.Stdin != nil {
			b, err := io.ReadAll(cmd.Stdin)
			require.NoError(t, err)
			stdinBytes = b
		}
		return nil
	}

	val, err := securebytes.New([]byte("payload-bytes"))
	require.NoError(t, err)
	defer val.Destroy()

	require.NoError(t, k.Store(context.Background(), "svc", "acct", val, "/abs/hush"))

	require.Equal(t, []string{
		"/usr/bin/security",
		"add-generic-password",
		"-s", "svc",
		"-a", "acct",
		"-T", "/abs/hush",
		"-w",
	}, captured)

	require.Equal(t, []byte("payload-bytes"), stdinBytes)
	require.NotContains(t, captured, "-A", "no allow-all flag")
}

func TestKeychainDarwin_StoreReturnsItemExistsOn45(t *testing.T) {
	t.Parallel()
	k := newDarwinForTest()
	k.runFn = func(*exec.Cmd) error { return fakeExitErr(t, exitDuplicateItem) }

	val, err := securebytes.New([]byte("bytes"))
	require.NoError(t, err)
	defer val.Destroy()

	got := k.Store(context.Background(), "svc", "acct", val, "/abs/hush")
	require.True(t, errors.Is(got, ErrKeychainItemExists))
}

func TestKeychainDarwin_RetrieveExitCode44IsNotFound(t *testing.T) {
	t.Parallel()
	k := newDarwinForTest()
	k.outputFn = func(*exec.Cmd) ([]byte, error) {
		return nil, fakeExitErr(t, exitItemNotFound)
	}

	_, err := k.Retrieve(context.Background(), "svc", "acct")
	require.True(t, errors.Is(err, ErrKeychainItemNotFound))
}

// TestKeychainDarwin_RetrieveDenialCodes asserts every Darwin exit
// code the OS returns for "item exists but the read was refused"
// collapses to [ErrKeychainPermissionDenied]. Plan AC-5 / Task 3.1:
// init's ACL-aware recovery flow branches on this single sentinel.
func TestKeychainDarwin_RetrieveDenialCodes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		exit int
	}{
		{"errSecInteractionNotAllowed", exitInteractionNotAllowed},
		{"errSecAuthFailed", exitAuthFailed},
		{"errSecUserCanceled", exitUserCanceled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			k := newDarwinForTest()
			k.outputFn = func(*exec.Cmd) ([]byte, error) {
				return nil, fakeExitErr(t, tc.exit)
			}

			_, err := k.Retrieve(context.Background(), "svc", "acct")
			require.True(t, errors.Is(err, ErrKeychainPermissionDenied),
				"exit %d (%s) must map to ErrKeychainPermissionDenied; got %v",
				tc.exit, tc.name, err)
		})
	}
}

func TestKeychainDarwin_RetrieveParsesStdoutPayload(t *testing.T) {
	t.Parallel()
	k := newDarwinForTest()
	k.outputFn = func(*exec.Cmd) ([]byte, error) { return []byte("retrieved-bytes\n"), nil }

	got, err := k.Retrieve(context.Background(), "svc", "acct")
	require.NoError(t, err)
	defer got.Destroy()

	require.NoError(t, got.Use(func(b []byte) {
		require.Equal(t, []byte("retrieved-bytes"), b)
	}))
}

func TestKeychainDarwin_DeleteSucceedsAndIsNotIdempotent(t *testing.T) {
	t.Parallel()
	k := newDarwinForTest()
	calls := 0
	k.runFn = func(*exec.Cmd) error {
		calls++
		if calls == 1 {
			return nil
		}
		return fakeExitErr(t, exitItemNotFound)
	}

	require.NoError(t, k.Delete(context.Background(), "svc", "acct"))
	err := k.Delete(context.Background(), "svc", "acct")
	require.True(t, errors.Is(err, ErrKeychainItemNotFound))
}

func TestKeychainDarwin_StoreSecretViaStdinNotArgv(t *testing.T) {
	t.Parallel()
	k := newDarwinForTest()
	const sentinel = "PROC_LISTING_LEAK"
	var args []string
	var stdin string
	k.runFn = func(cmd *exec.Cmd) error {
		args = append([]string(nil), cmd.Args...)
		if cmd.Stdin != nil {
			b, _ := io.ReadAll(cmd.Stdin)
			stdin = string(b)
		}
		return nil
	}

	val, err := securebytes.New([]byte(sentinel))
	require.NoError(t, err)
	defer val.Destroy()

	require.NoError(t, k.Store(context.Background(), "svc", "acct", val, "/abs/hush"))
	for _, a := range args {
		require.NotContains(t, a, sentinel, "secret leaked into argv")
	}
	require.Equal(t, sentinel, stdin)
}

func TestPerBinaryACLSupported_Darwin(t *testing.T) {
	t.Parallel()
	require.True(t, PerBinaryACLSupported())
}

func TestNewPlatformKeychain_Darwin(t *testing.T) {
	t.Parallel()
	kc, err := newPlatformKeychain(slog.Default())
	require.NoError(t, err)
	require.NotNil(t, kc)
	_, ok := kc.(*darwinKeychain)
	require.True(t, ok)
}

func TestMapSecurityError_NonExitError(t *testing.T) {
	t.Parallel()
	got := mapSecurityError(io.EOF, "store")
	require.False(t, errors.Is(got, ErrKeychainItemNotFound))
	require.False(t, errors.Is(got, ErrKeychainItemExists))
	require.False(t, errors.Is(got, ErrKeychainPermissionDenied))
}
