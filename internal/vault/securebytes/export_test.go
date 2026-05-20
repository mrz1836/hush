package securebytes

import "golang.org/x/sys/unix"

// SetRlimitHooks replaces the rlimit bridges for the duration of a test.
// Returns a cleanup function that restores the originals.
func SetRlimitHooks(get, set func(int, *unix.Rlimit) error) func() {
	origGet, origSet := getrlimitFn, setrlimitFn
	getrlimitFn, setrlimitFn = get, set
	return func() { getrlimitFn, setrlimitFn = origGet, origSet }
}

// RaiseMemlockLimit exposes raiseMemlockLimit for tests.
func RaiseMemlockLimit() { raiseMemlockLimit() }

// SetMLock replaces the mlock bridge for the duration of a test.
// Returns a cleanup function that restores the original.
func SetMLock(f func([]byte) error) func() {
	orig := mlockFn
	mlockFn = f
	return func() { mlockFn = orig }
}

// SetMUnlock replaces the munlock bridge for the duration of a test.
// Returns a cleanup function that restores the original.
func SetMUnlock(f func([]byte) error) func() {
	orig := munlockFn
	munlockFn = f
	return func() { munlockFn = orig }
}
