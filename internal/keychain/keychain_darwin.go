//go:build darwin

package keychain

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"

	"github.com/mrz1836/hush/internal/vault/securebytes"
)

const (
	securityBin = "/usr/bin/security"

	// Apple SecKeychain error codes; see SecBase.h.
	//
	// /usr/bin/security returns the low byte of the OSStatus value as
	// its process exit code. Each constant below names the symbolic
	// OSStatus and pins the truncated exit code we observe in tests
	// and at runtime. Renaming or reordering changes ABI for the
	// guided flow; see [mapSecurityError] for the mapping into the
	// public keychain sentinels.
	exitInteractionNotAllowed = 36  // errSecInteractionNotAllowed (-25308)
	exitItemNotFound          = 44  // errSecItemNotFound (-25300)
	exitDuplicateItem         = 45  // errSecDuplicateItem (-25299)
	exitAuthFailed            = 51  // errSecAuthFailed (-25293)
	exitUserCanceled          = 128 // errSecUserCanceled (-128)
)

// runner is the test seam for executing /usr/bin/security. Production
// path runs cmd.Run; tests inject a recorder that captures argv and
// stdin without launching the real binary.
type runner func(*exec.Cmd) error

// outputRunner runs cmd and returns its stdout; tests inject a
// recorder that returns programmed bytes.
type outputRunner func(*exec.Cmd) ([]byte, error)

// darwinKeychain is the macOS implementation. Operations shell out
// to /usr/bin/security with a fixed argv vector; secrets travel via
// stdin to keep them out of the process listing.
type darwinKeychain struct {
	logger   *slog.Logger
	binary   string
	runFn    runner
	outputFn outputRunner
}

func newPlatformKeychain(logger *slog.Logger) (Keychain, error) {
	return &darwinKeychain{
		logger:   logger,
		binary:   securityBin,
		runFn:    (*exec.Cmd).Run,
		outputFn: (*exec.Cmd).Output,
	}, nil
}

// Store creates a new generic-password item under (service, account)
// with the supplied per-binary ACL. The secret never appears in
// argv; it is written to security's stdin via the -w flag.
func (k *darwinKeychain) Store(ctx context.Context, service, account string, value *securebytes.SecureBytes, acl string) error {
	cmd := exec.CommandContext(ctx, k.binary, //nolint:gosec // fixed argv; service/account/acl are caller-vetted
		"add-generic-password",
		"-s", service,
		"-a", account,
		"-T", acl,
		"-w",
	)
	var feedErr error
	if useErr := value.Use(func(b []byte) {
		// Copy into a fresh buffer so the cmd-internal io.Reader
		// does not retain the SecureBytes' mlocked slice.
		buf := make([]byte, len(b))
		copy(buf, b)
		cmd.Stdin = bytes.NewReader(buf)
	}); useErr != nil {
		feedErr = useErr
	}
	if feedErr != nil {
		return feedErr
	}
	if err := k.runFn(cmd); err != nil {
		return mapSecurityError(err, "store")
	}
	return nil
}

// Retrieve fetches the stored secret via find-generic-password.
func (k *darwinKeychain) Retrieve(ctx context.Context, service, account string) (*securebytes.SecureBytes, error) {
	cmd := exec.CommandContext(ctx, k.binary, //nolint:gosec // fixed argv
		"find-generic-password",
		"-s", service,
		"-a", account,
		"-w",
	)
	out, err := k.outputFn(cmd)
	if err != nil {
		return nil, mapSecurityError(err, "retrieve")
	}
	out = stripTrailingNewline(out)
	if len(out) == 0 {
		return nil, ErrKeychainItemNotFound
	}
	return securebytes.New(out)
}

// Delete removes the keychain item.
func (k *darwinKeychain) Delete(ctx context.Context, service, account string) error {
	cmd := exec.CommandContext(ctx, k.binary, //nolint:gosec // fixed argv
		"delete-generic-password",
		"-s", service,
		"-a", account,
	)
	if err := k.runFn(cmd); err != nil {
		return mapSecurityError(err, "delete")
	}
	return nil
}

// mapSecurityError maps the exit code from /usr/bin/security to a
// keychain sentinel. The three denial codes
// ([exitInteractionNotAllowed], [exitAuthFailed], [exitUserCanceled])
// collapse to [ErrKeychainPermissionDenied]; init's ACL-aware
// recovery flow re-translates that sentinel into
// [setup.ErrTokenDenied] when the read targets the bot-token item
// (Plan AC-5 / Task 3.1).
func mapSecurityError(err error, op string) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		switch ee.ExitCode() {
		case exitItemNotFound:
			return ErrKeychainItemNotFound
		case exitDuplicateItem:
			return ErrKeychainItemExists
		case exitInteractionNotAllowed, exitAuthFailed, exitUserCanceled:
			return ErrKeychainPermissionDenied
		}
	}
	return fmt.Errorf("hush/keychain: security %s: %w", op, err)
}

// stripTrailingNewline removes exactly one trailing \n if present.
func stripTrailingNewline(b []byte) []byte {
	if n := len(b); n > 0 && b[n-1] == '\n' {
		return b[:n-1]
	}
	return b
}
