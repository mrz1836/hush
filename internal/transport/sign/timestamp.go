package sign

import "time"

//nolint:gochecknoglobals // test-swappable clock per Constitution IX (sentinel-class precedent)
var nowFn = time.Now

// IsFreshTimestamp reports whether ts is within ±skew of now. A non-positive
// skew always returns false. The boundary value (|delta| == skew) is
// accepted (≤ semantics).
func IsFreshTimestamp(ts time.Time, skew time.Duration) bool {
	if skew <= 0 {
		return false
	}
	delta := nowFn().Sub(ts)
	if delta < 0 {
		delta = -delta
	}
	return delta <= skew
}
