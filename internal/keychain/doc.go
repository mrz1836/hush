// Package keychain wraps the platform-native OS keychain operations
// behind a single Keychain interface. The Darwin implementation
// shells out to /usr/bin/security with a per-binary `-T` ACL flag;
// the Linux implementation wraps github.com/zalando/go-keyring and
// is provided for cross-platform compilation only because the Linux
// Secret Service has no per-binary ACL primitive.
//
// Production callers MUST gate via PerBinaryACLSupported() and
// refuse to operate on platforms where it returns false.
//
// Sentinel errors: ErrKeychainItemNotFound, ErrKeychainItemExists,
// ErrKeychainPermissionDenied, ErrKeychainUnsupportedPlatform.
package keychain
