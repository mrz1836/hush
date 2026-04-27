package securebytes

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
