package testutil

import (
	"sync/atomic"
	"time"
)

// FakeClock is a test-only injectable monotonic clock. It stores time
// as a unix-nano int64 and is safe for concurrent Now/Advance/SetTo
// without locking.
type FakeClock struct {
	v atomic.Int64
}

// NewFakeClock returns a FakeClock anchored at start.
func NewFakeClock(start time.Time) *FakeClock {
	c := &FakeClock{}
	c.v.Store(start.UnixNano())
	return c
}

// Now returns the current fake time.
func (c *FakeClock) Now() time.Time {
	return time.Unix(0, c.v.Load())
}

// Advance moves the clock forward by d.
func (c *FakeClock) Advance(d time.Duration) {
	c.v.Add(int64(d))
}

// SetTo pins the clock to t.
func (c *FakeClock) SetTo(t time.Time) {
	c.v.Store(t.UnixNano())
}

// NowFn returns a closure backed by this FakeClock — suitable for
// injection into APIs that take a func() time.Time.
func (c *FakeClock) NowFn() func() time.Time {
	return c.Now
}
