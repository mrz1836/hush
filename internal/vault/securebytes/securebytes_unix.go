//go:build unix

package securebytes

import "golang.org/x/sys/unix"

func mlock(b []byte) error {
	return unix.Mlock(b)
}

func munlock(b []byte) error {
	return unix.Munlock(b)
}

// getrlimitFn / setrlimitFn are the active OS rlimit bridges; set once
// at startup, replaced in tests for error-path coverage.
var (
	getrlimitFn = unix.Getrlimit //nolint:gochecknoglobals // OS bridge; test-hookable for error-path coverage
	setrlimitFn = unix.Setrlimit //nolint:gochecknoglobals // OS bridge; test-hookable for error-path coverage
)

// raiseMemlockLimit lifts the process soft RLIMIT_MEMLOCK ceiling to the
// hard limit when the soft limit is the lower of the two. It runs once,
// before the first mlock, via raiseOnce in New.
//
// SecureBytes pins every payload with mlock; the OS default soft limit
// (commonly 8 MiB on Linux) would otherwise cap total locked memory and
// make a vault larger than that fail to load with ENOMEM. The raise only
// ever moves the soft limit up to the already-permitted hard limit, so it
// cannot grant more locked memory than the OS already allows.
//
// It is best-effort: any syscall failure leaves the limit untouched, and
// mlock still reports ENOMEM loudly rather than silently degrading.
func raiseMemlockLimit() {
	var lim unix.Rlimit
	if err := getrlimitFn(unix.RLIMIT_MEMLOCK, &lim); err != nil {
		return
	}
	if lim.Cur >= lim.Max {
		return
	}
	lim.Cur = lim.Max
	_ = setrlimitFn(unix.RLIMIT_MEMLOCK, &lim)
}
