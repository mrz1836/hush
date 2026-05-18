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
	k.outputFn = func(cmd *exec.Cmd) ([]byte, error) {
		captured = append([]string(nil), cmd.Args...)
		require.Equal(t, "/usr/bin/security", cmd.Path)
		require.Nil(t, cmd.Stdin, "security -w requires an argv value; stdin would trigger Apple's raw prompt")
		return nil, nil
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
		"-w", "payload-bytes",
	}, captured)

	require.NotContains(t, captured, "-A", "no allow-all flag")
}

func TestKeychainDarwin_StoreReturnsItemExistsOn45(t *testing.T) {
	t.Parallel()
	k := newDarwinForTest()
	k.outputFn = func(*exec.Cmd) ([]byte, error) { return nil, fakeExitErr(t, exitDuplicateItem) }

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
	k.outputFn = func(*exec.Cmd) ([]byte, error) {
		calls++
		if calls == 1 {
			return nil, nil
		}
		return nil, fakeExitErr(t, exitItemNotFound)
	}

	require.NoError(t, k.Delete(context.Background(), "svc", "acct"))
	err := k.Delete(context.Background(), "svc", "acct")
	require.True(t, errors.Is(err, ErrKeychainItemNotFound))
}

func TestKeychainDarwin_StoreUsesPasswordArgToAvoidRawPrompt(t *testing.T) {
	t.Parallel()
	k := newDarwinForTest()
	const sentinel = "TOKEN_FOR_SECURITY_W_FLAG"
	var args []string
	k.outputFn = func(cmd *exec.Cmd) ([]byte, error) {
		args = append([]string(nil), cmd.Args...)
		require.Nil(t, cmd.Stdin, "stdin is ignored by security add-generic-password -w and triggers raw prompts")
		return nil, nil
	}

	val, err := securebytes.New([]byte(sentinel))
	require.NoError(t, err)
	defer val.Destroy()

	require.NoError(t, k.Store(context.Background(), "svc", "acct", val, "/abs/hush"))
	require.Contains(t, args, "-w")
	require.Contains(t, args, sentinel)
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

// TestT273_Fixture1_RetrieveExit51IsPermissionDenied is the dedicated
// SC-10 / AC-12 case 1 fixture at the keychain layer: Darwin exit
// code 51 (errSecAuthFailed — the "item exists but the read was
// refused" verdict observed during T-273) MUST map to
// [ErrKeychainPermissionDenied]. Upstream this drives
// [setup.ErrTokenDenied] and the ACL-repair panel; pinning the wire
// here means a future kernel-level remap of exit codes cannot quietly
// regress the panel-trigger condition.
func TestT273_Fixture1_RetrieveExit51IsPermissionDenied(t *testing.T) {
	t.Parallel()
	k := newDarwinForTest()
	k.outputFn = func(*exec.Cmd) ([]byte, error) {
		return nil, fakeExitErr(t, exitAuthFailed)
	}

	_, err := k.Retrieve(context.Background(), "hush-discord", "hush-server")
	require.True(t, errors.Is(err, ErrKeychainPermissionDenied),
		"exit %d (errSecAuthFailed) MUST map to ErrKeychainPermissionDenied; got %v",
		exitAuthFailed, err)
	require.Equal(t, 51, exitAuthFailed,
		"exit 51 is the observed T-273 ACL-denial code; renaming the constant must keep its value")
}

func TestMapSecurityError_NonExitError(t *testing.T) {
	t.Parallel()
	got := mapSecurityError(io.EOF, "store")
	require.False(t, errors.Is(got, ErrKeychainItemNotFound))
	require.False(t, errors.Is(got, ErrKeychainItemExists))
	require.False(t, errors.Is(got, ErrKeychainPermissionDenied))
}
