package vault

import (
	"crypto/cipher"
	"io"
	"os"

	"github.com/mrz1836/hush/internal/vault/securebytes"
)

// SetRandRead replaces the crypto/rand bridge. Returns a restore function.
func SetRandRead(f func([]byte) (int, error)) func() {
	orig := randRead
	randRead = f
	return func() { randRead = orig }
}

// SetOSOpenFile replaces the os.OpenFile bridge. Returns a restore function.
func SetOSOpenFile(f func(string, int, os.FileMode) (*os.File, error)) func() {
	orig := osOpenFile
	osOpenFile = f
	return func() { osOpenFile = orig }
}

// SetOSRename replaces the os.Rename bridge. Returns a restore function.
func SetOSRename(f func(string, string) error) func() {
	orig := osRename
	osRename = f
	return func() { osRename = orig }
}

// SetOSChmod replaces the os.Chmod bridge. Returns a restore function.
func SetOSChmod(f func(string, os.FileMode) error) func() {
	orig := osChmod
	osChmod = f
	return func() { osChmod = orig }
}

// SetOSRemoveFn replaces the os.Remove bridge. Returns a restore function.
func SetOSRemoveFn(f func(string) error) func() {
	orig := osRemoveFn
	osRemoveFn = f
	return func() { osRemoveFn = orig }
}

// SetIOReadAllFn replaces the io.ReadAll bridge. Returns a restore function.
func SetIOReadAllFn(f func(io.Reader) ([]byte, error)) func() {
	orig := ioReadAllFn
	ioReadAllFn = f
	return func() { ioReadAllFn = orig }
}

// SetFileWrite replaces the (*os.File).Write bridge. Returns a restore function.
func SetFileWrite(f func(*os.File, []byte) (int, error)) func() {
	orig := fileWrite
	fileWrite = f
	return func() { fileWrite = orig }
}

// SetFileSync replaces the (*os.File).Sync bridge. Returns a restore function.
func SetFileSync(f func(*os.File) error) func() {
	orig := fileSync
	fileSync = f
	return func() { fileSync = orig }
}

// SetFileClose replaces the (*os.File).Close bridge. Returns a restore function.
func SetFileClose(f func(*os.File) error) func() {
	orig := fileClose
	fileClose = f
	return func() { fileClose = orig }
}

// SetSBDestroyFn replaces the SecureBytes destroy bridge. Returns a restore function.
func SetSBDestroyFn(f func(*securebytes.SecureBytes) error) func() {
	orig := sbDestroyFn
	sbDestroyFn = f
	return func() { sbDestroyFn = orig }
}

// SetSBNewFn replaces the securebytes.New bridge in Get. Returns a restore function.
func SetSBNewFn(f func([]byte) (*securebytes.SecureBytes, error)) func() {
	orig := sbNewFn
	sbNewFn = f
	return func() { sbNewFn = orig }
}

// SetSBNewFromUnmarshalFn replaces the securebytes.New bridge in UnmarshalJSON.
func SetSBNewFromUnmarshalFn(f func([]byte) (*securebytes.SecureBytes, error)) func() {
	orig := sbNewFromUnmarshal
	sbNewFromUnmarshal = f
	return func() { sbNewFromUnmarshal = orig }
}

// SetNewAEAD replaces the newAEAD bridge. Returns a restore function.
func SetNewAEAD(f func([]byte) (cipher.AEAD, error)) func() {
	orig := newAEAD
	newAEAD = f
	return func() { newAEAD = orig }
}

// ForceDestroyInternalContainer directly destroys a named secret's internal
// *SecureBytes to simulate a race-with-Destroy scenario in Store.Get tests.
func ForceDestroyInternalContainer(store Store, name string) {
	s := store.(*memStore)
	s.mu.RLock()
	defer s.mu.RUnlock()
	if sb, ok := s.byName[name]; ok {
		_ = sb.Destroy()
	}
}

// Test-export shims — expose internal helpers for direct unit tests.
//
//nolint:gochecknoglobals // test-export shims; required for white-box testing
var (
	// CheckFileMode exposes the internal checkFileMode helper for direct unit tests.
	CheckFileMode = checkFileMode

	// CheckParentMode exposes the internal checkParentMode helper for direct unit tests.
	CheckParentMode = checkParentMode

	// ParseAndDecrypt exposes the internal parseAndDecrypt helper for direct unit tests.
	ParseAndDecrypt = parseAndDecrypt

	// WriteTmp exposes the internal writeTmp helper for direct unit tests.
	WriteTmp = writeTmp
)
