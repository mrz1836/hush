package server

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func TestCachedClockSyncProbe_WritesSuccessfulLiveProbe(t *testing.T) {
	stateDir := rwxStateDir(t)
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	probe := CachedClockSyncProbe(
		scriptedClockProbe(true, 42*time.Millisecond, nil),
		stateDir,
		func() time.Time { return now },
		nil,
	)

	synced, drift, err := probe(context.Background())
	if err != nil {
		t.Fatalf("probe err=%v", err)
	}
	if !synced || drift != 42*time.Millisecond {
		t.Fatalf("synced=%v drift=%v", synced, drift)
	}

	info, err := os.Stat(clockSyncCachePath(stateDir))
	if err != nil {
		t.Fatalf("stat cache: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("cache mode=%#o want 0600", mode)
	}
	entry, err := readClockSyncCache(stateDir)
	if err != nil {
		t.Fatalf("read cache: %v", err)
	}
	if entry.DriftNS != int64(42*time.Millisecond) || !entry.MeasuredAt.Equal(now) {
		t.Fatalf("entry=%+v", entry)
	}
}

func TestCachedClockSyncProbe_FreshCacheFallback(t *testing.T) {
	stateDir := rwxStateDir(t)
	measuredAt := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	requireClockSyncCacheWrite(t, stateDir, clockSyncCacheEntry{
		DriftNS:    int64(-75 * time.Millisecond),
		MeasuredAt: measuredAt,
	})

	var fallback ClockSyncCacheFallback
	probe := CachedClockSyncProbe(
		scriptedClockProbe(false, 0, ErrClockProbeUnavailable),
		stateDir,
		func() time.Time { return measuredAt.Add(5 * time.Minute) },
		func(_ context.Context, fb ClockSyncCacheFallback) { fallback = fb },
	)

	synced, drift, err := probe(context.Background())
	if err != nil {
		t.Fatalf("probe err=%v", err)
	}
	if !synced || drift != -75*time.Millisecond {
		t.Fatalf("synced=%v drift=%v", synced, drift)
	}
	if fallback.Age != 5*time.Minute || fallback.Drift != -75*time.Millisecond || !fallback.MeasuredAt.Equal(measuredAt) {
		t.Fatalf("fallback=%+v", fallback)
	}
}

func TestCachedClockSyncProbe_StaleCacheSurfacesUnavailable(t *testing.T) {
	stateDir := rwxStateDir(t)
	measuredAt := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	requireClockSyncCacheWrite(t, stateDir, clockSyncCacheEntry{
		DriftNS:    int64(10 * time.Millisecond),
		MeasuredAt: measuredAt,
	})
	probe := CachedClockSyncProbe(
		scriptedClockProbe(false, 0, ErrClockProbeUnavailable),
		stateDir,
		func() time.Time { return measuredAt.Add(DefaultClockSyncCacheMaxAge + time.Nanosecond) },
		nil,
	)

	_, _, err := probe(context.Background())
	if !errors.Is(err, ErrClockProbeUnavailable) {
		t.Fatalf("err=%v want ErrClockProbeUnavailable", err)
	}
}

func TestCachedClockSyncProbe_AbsentCacheSurfacesUnavailable(t *testing.T) {
	probe := CachedClockSyncProbe(
		scriptedClockProbe(false, 0, ErrClockProbeUnavailable),
		rwxStateDir(t),
		func() time.Time { return time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC) },
		nil,
	)

	_, _, err := probe(context.Background())
	if !errors.Is(err, ErrClockProbeUnavailable) {
		t.Fatalf("err=%v want ErrClockProbeUnavailable", err)
	}
}

func requireClockSyncCacheWrite(t *testing.T, stateDir string, entry clockSyncCacheEntry) {
	t.Helper()
	if err := writeClockSyncCache(stateDir, entry); err != nil {
		t.Fatalf("write cache: %v", err)
	}
}
