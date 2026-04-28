package sign

import (
	"testing"
	"time"
)

func TestTimestamp_FreshAccepted(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	setClockForTest(t, t0)

	if !IsFreshTimestamp(t0, 30*time.Second) {
		t.Error("exact-now should be fresh")
	}
	if !IsFreshTimestamp(t0.Add(-29*time.Second), 30*time.Second) {
		t.Error("29s old should be fresh within 30s skew")
	}
}

func TestTimestamp_TooOldRejected(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	setClockForTest(t, t0)

	if IsFreshTimestamp(t0.Add(-31*time.Second), 30*time.Second) {
		t.Error("31s old should be stale with 30s skew")
	}
}

func TestTimestamp_FutureSkewRejected(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	setClockForTest(t, t0)

	if IsFreshTimestamp(t0.Add(31*time.Second), 30*time.Second) {
		t.Error("31s in the future should be stale with 30s skew")
	}
}

func TestTimestamp_BoundaryAccepted(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	setClockForTest(t, t0)

	// delta == skew is accepted (≤ semantics)
	if !IsFreshTimestamp(t0.Add(30*time.Second), 30*time.Second) {
		t.Error("exactly +30s boundary should be accepted with 30s skew")
	}
	if !IsFreshTimestamp(t0.Add(-30*time.Second), 30*time.Second) {
		t.Error("exactly -30s boundary should be accepted with 30s skew")
	}
}

func TestTimestamp_NonPositiveSkewRejected(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	setClockForTest(t, t0)

	if IsFreshTimestamp(t0, 0) {
		t.Error("zero skew should always return false")
	}
	if IsFreshTimestamp(t0, -1*time.Second) {
		t.Error("negative skew should always return false")
	}
}
