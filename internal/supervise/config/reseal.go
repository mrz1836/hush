package config

import (
	"time"
)

// NextRequestedTTL returns the requested TTL needed to land on the next
// configured reseal slot, capped by MaxRequestedTTL. If the next civil slot is
// inside ResealMinSessionFloor, it is skipped; with a 24h ceiling this can make
// a boot just before the slot settle into a daily pre-slot renewal rhythm.
func (r *ResealSchedule) NextRequestedTTL(now time.Time) time.Duration {
	next := r.NextReseal(now)
	if next.IsZero() {
		return 0
	}
	ttl := next.Sub(now.In(r.Location))
	if ttl > MaxRequestedTTL {
		return MaxRequestedTTL
	}
	return ttl
}

// NextReseal returns the next configured reseal instant after now that is not
// inside ResealMinSessionFloor.
func (r *ResealSchedule) NextReseal(now time.Time) time.Time {
	if r == nil || r.Location == nil {
		return time.Time{}
	}

	localNow := now.In(r.Location)
	// A valid daily schedule should resolve within at most two iterations:
	// today or tomorrow. The wider bound is a defensive backstop around unusual
	// civil-time transitions and programmatically constructed values.
	for day := 0; day <= 8; day++ {
		slotDate := localNow.AddDate(0, 0, day)
		slot := r.slotFor(slotDate.Weekday())
		candidate := time.Date(
			slotDate.Year(),
			slotDate.Month(),
			slotDate.Day(),
			slot.Hour,
			slot.Minute,
			0,
			0,
			r.Location,
		)
		if !candidate.After(localNow) {
			continue
		}
		if candidate.Sub(localNow) < ResealMinSessionFloor {
			continue
		}
		return candidate
	}
	return time.Time{}
}

func (r *ResealSchedule) slotFor(weekday time.Weekday) hhmm {
	if override, ok := r.Overrides[weekday]; ok {
		return override
	}
	return r.DailyTime
}
