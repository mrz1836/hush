package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type clockSyncCacheEntry struct {
	DriftNS    int64     `json:"drift_ns"`
	MeasuredAt time.Time `json:"measured_at"`
}

type ClockSyncCacheFallback struct {
	Drift      time.Duration
	MeasuredAt time.Time
	Age        time.Duration
}

// CachedClockSyncProbe wraps a live probe with a recent-good cache. The live
// probe remains authoritative; the cache is read only when every provider is
// unavailable.
func CachedClockSyncProbe(
	probe func(ctx context.Context) (synced bool, drift time.Duration, err error),
	stateDir string,
	now func() time.Time,
	onFallback func(context.Context, ClockSyncCacheFallback),
) func(ctx context.Context) (synced bool, drift time.Duration, err error) {
	if probe == nil {
		probe = DefaultClockSyncProbe
	}
	if now == nil {
		now = time.Now
	}
	return func(ctx context.Context) (bool, time.Duration, error) {
		synced, drift, err := probe(ctx)
		if err == nil {
			if synced && stateDir != "" {
				_ = writeClockSyncCache(stateDir, clockSyncCacheEntry{
					DriftNS:    int64(drift),
					MeasuredAt: now().UTC(),
				})
			}
			return synced, drift, nil
		}
		if !errors.Is(err, ErrClockProbeUnavailable) || stateDir == "" {
			return false, 0, err
		}
		entry, cacheErr := readClockSyncCache(stateDir)
		if cacheErr != nil {
			return false, 0, err
		}
		age := now().UTC().Sub(entry.MeasuredAt.UTC())
		if age < 0 || age > DefaultClockSyncCacheMaxAge {
			return false, 0, err
		}
		fallback := ClockSyncCacheFallback{
			Drift:      time.Duration(entry.DriftNS),
			MeasuredAt: entry.MeasuredAt.UTC(),
			Age:        age,
		}
		if onFallback != nil {
			onFallback(ctx, fallback)
		}
		return true, fallback.Drift, nil
	}
}

func clockSyncCachePath(stateDir string) string {
	return filepath.Join(stateDir, clockSyncCacheFilename)
}

func writeClockSyncCache(stateDir string, entry clockSyncCacheEntry) error {
	b, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("clock sync cache marshal: %w", err)
	}
	p := clockSyncCachePath(stateDir)
	if err := os.WriteFile(p, append(b, '\n'), 0o600); err != nil {
		return fmt.Errorf("clock sync cache write: %w", err)
	}
	if err := os.Chmod(p, 0o600); err != nil {
		return fmt.Errorf("clock sync cache chmod: %w", err)
	}
	return nil
}

func readClockSyncCache(stateDir string) (clockSyncCacheEntry, error) {
	var entry clockSyncCacheEntry
	b, err := os.ReadFile(clockSyncCachePath(stateDir))
	if err != nil {
		return entry, fmt.Errorf("clock sync cache read: %w", err)
	}
	if err := json.Unmarshal(b, &entry); err != nil {
		return entry, fmt.Errorf("clock sync cache decode: %w", err)
	}
	if entry.MeasuredAt.IsZero() {
		return entry, errors.New("clock sync cache missing measurement time")
	}
	return entry, nil
}
