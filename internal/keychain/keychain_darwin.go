//go:build darwin

package keychain

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"

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

// outputRunner runs cmd and returns its stdout/stderr as configured by the
// caller; tests inject a recorder that returns programmed bytes.
type outputRunner func(*exec.Cmd) ([]byte, error)

// darwinKeychain is the macOS implementation. Operations shell out
// to /usr/bin/security with a fixed argv vector.
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
		outputFn: (*exec.Cmd).CombinedOutput,
	}, nil
}

// Store creates a new generic-password item under (service, account)
// with the supplied per-binary ACL.
//
// macOS `security add-generic-password -w` does not read the password from
// stdin; when `-w` has no following argument it drops into the raw interactive
// "password data for new item" prompt. That prompt is exactly the UX hush must
// hide behind its guided panels, so Store passes the value as the `-w` argument.
// This briefly exposes the token to local process-list observers on the trusted
// vault host; the alternative is an unavoidable Apple prompt that breaks the
// guided setup UX. Keep this bridge isolated here so a future native
// Security.framework implementation can remove the argv exposure without
// touching CLI flow.
func (k *darwinKeychain) Store(ctx context.Context, service, account string, value *securebytes.SecureBytes, acl string) error {
	var password string
	if useErr := value.Use(func(b []byte) {
		password = string(b)
	}); useErr != nil {
		return useErr
	}
	defer func() { password = "" }()

	cmd := exec.CommandContext(ctx, k.binary, //nolint:gosec // fixed argv; see Store doc for -w password trade-off
		"add-generic-password",
		"-s", service,
		"-a", account,
		"-T", acl,
		"-w", password,
	)
	out, err := k.outputFn(cmd)
	if err != nil {
		return mapSecurityError(err, "store", string(out))
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
		return nil, mapSecurityError(err, "retrieve", string(out))
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
	out, err := k.outputFn(cmd)
	if err != nil {
		return mapSecurityError(err, "delete", string(out))
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
func mapSecurityError(err error, op string, output ...string) error {
	detail := strings.TrimSpace(strings.Join(output, "\n"))
	wrap := func(base error) error {
		if detail == "" {
			return base
		}
		return fmt.Errorf("%w: %s", base, detail)
	}
	if strings.Contains(strings.ToLower(detail), "passphrase") || strings.Contains(strings.ToLower(detail), "keychain is locked") {
		return wrap(ErrKeychainLocked)
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		switch ee.ExitCode() {
		case exitItemNotFound:
			return wrap(ErrKeychainItemNotFound)
		case exitDuplicateItem:
			return wrap(ErrKeychainItemExists)
		case exitInteractionNotAllowed, exitAuthFailed, exitUserCanceled:
			return wrap(ErrKeychainPermissionDenied)
		}
	}
	if detail != "" {
		return fmt.Errorf("hush/keychain: security %s: %w: %s", op, err, detail)
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
