package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResealScheduleNextRequestedTTL(t *testing.T) {
	t.Parallel()
	loc := mustLocation(t, "America/New_York")

	tests := []struct {
		name     string
		schedule *ResealSchedule
		now      time.Time
		next     time.Time
		ttl      time.Duration
	}{
		{
			name: "before slot lands exactly on same day",
			schedule: &ResealSchedule{
				Location:  loc,
				DailyTime: hhmm{Hour: 10, Minute: 0},
			},
			now:  time.Date(2026, 1, 12, 8, 30, 0, 0, loc),
			next: time.Date(2026, 1, 12, 10, 0, 0, 0, loc),
			ttl:  90 * time.Minute,
		},
		{
			name: "after slot lands exactly on next day",
			schedule: &ResealSchedule{
				Location:  loc,
				DailyTime: hhmm{Hour: 10, Minute: 0},
			},
			now:  time.Date(2026, 1, 12, 10, 1, 0, 0, loc),
			next: time.Date(2026, 1, 13, 10, 0, 0, 0, loc),
			ttl:  23*time.Hour + 59*time.Minute,
		},
		{
			name: "boot just before slot skips floor and clamps",
			schedule: &ResealSchedule{
				Location:  loc,
				DailyTime: hhmm{Hour: 3, Minute: 0},
			},
			now:  time.Date(2026, 1, 12, 2, 58, 0, 0, loc),
			next: time.Date(2026, 1, 13, 3, 0, 0, 0, loc),
			ttl:  MaxRequestedTTL,
		},
		{
			name: "heterogeneous week friday to saturday override",
			schedule: &ResealSchedule{
				Location:  loc,
				DailyTime: hhmm{Hour: 10, Minute: 0},
				Overrides: map[time.Weekday]hhmm{
					time.Saturday: {Hour: 14, Minute: 0},
				},
			},
			now:  time.Date(2026, 1, 16, 11, 0, 0, 0, loc),
			next: time.Date(2026, 1, 17, 14, 0, 0, 0, loc),
			ttl:  MaxRequestedTTL,
		},
		{
			name: "heterogeneous week saturday before override",
			schedule: &ResealSchedule{
				Location:  loc,
				DailyTime: hhmm{Hour: 10, Minute: 0},
				Overrides: map[time.Weekday]hhmm{
					time.Saturday: {Hour: 14, Minute: 0},
				},
			},
			now:  time.Date(2026, 1, 17, 13, 0, 0, 0, loc),
			next: time.Date(2026, 1, 17, 14, 0, 0, 0, loc),
			ttl:  time.Hour,
		},
		{
			name: "heterogeneous week saturday after override to sunday daily",
			schedule: &ResealSchedule{
				Location:  loc,
				DailyTime: hhmm{Hour: 10, Minute: 0},
				Overrides: map[time.Weekday]hhmm{
					time.Saturday: {Hour: 14, Minute: 0},
				},
			},
			now:  time.Date(2026, 1, 17, 14, 30, 0, 0, loc),
			next: time.Date(2026, 1, 18, 10, 0, 0, 0, loc),
			ttl:  19*time.Hour + 30*time.Minute,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.next, tt.schedule.NextReseal(tt.now))
			assert.Equal(t, tt.ttl, tt.schedule.NextRequestedTTL(tt.now))
		})
	}
}

func TestResealScheduleBootBeforeSlotSteadyState(t *testing.T) {
	t.Parallel()
	loc := mustLocation(t, "America/New_York")
	schedule := &ResealSchedule{
		Location:  loc,
		DailyTime: hhmm{Hour: 3, Minute: 0},
	}
	now := time.Date(2026, 1, 12, 2, 58, 0, 0, loc)

	for day := 0; day < 3; day++ {
		assert.Equal(t, MaxRequestedTTL, schedule.NextRequestedTTL(now))
		now = now.Add(schedule.NextRequestedTTL(now))
		assert.Equal(t, 2, now.In(loc).Hour())
		assert.Equal(t, 58, now.In(loc).Minute())
	}
}

func TestResealScheduleDSTWallClockSlots(t *testing.T) {
	t.Parallel()
	loc := mustLocation(t, "America/New_York")
	schedule := &ResealSchedule{
		Location:  loc,
		DailyTime: hhmm{Hour: 10, Minute: 0},
	}

	for _, tc := range []struct {
		name  string
		start time.Time
	}{
		{"spring forward", time.Date(2026, 3, 7, 0, 0, 0, 0, loc)},
		{"fall back", time.Date(2026, 10, 31, 0, 0, 0, 0, loc)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			for offset := 0; offset < 4; offset++ {
				dayStart := tc.start.AddDate(0, 0, offset)
				next := schedule.NextReseal(dayStart)
				assert.Equal(t, dayStart.Year(), next.In(loc).Year())
				assert.Equal(t, dayStart.YearDay(), next.In(loc).YearDay())
				assert.Equal(t, 10, next.In(loc).Hour())
				assert.Equal(t, 0, next.In(loc).Minute())

				afterSlot := next.Add(time.Minute)
				following := schedule.NextReseal(afterSlot)
				assert.Equal(t, dayStart.AddDate(0, 0, 1).YearDay(), following.In(loc).YearDay())
				assert.Equal(t, 10, following.In(loc).Hour())
				assert.Equal(t, 0, following.In(loc).Minute())
			}
		})
	}
}

func TestResealScheduleNilReceiver(t *testing.T) {
	t.Parallel()
	var schedule *ResealSchedule
	assert.Zero(t, schedule.NextRequestedTTL(time.Now()))
	assert.True(t, schedule.NextReseal(time.Now()).IsZero())
}

func mustLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	require.NoError(t, err)
	return loc
}
