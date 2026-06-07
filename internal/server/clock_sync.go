package server

import (
	"fmt"
	"regexp"
	"strconv"
	"time"
)

const (
	DefaultClockSyncProviderTimeout = 2 * time.Second
	DefaultClockSyncCacheMaxAge     = time.Hour

	clockSyncCacheFilename = "clock-sync.json"
)

// DefaultClockSyncProviders is the ordered first-success probe list used by
// the platform-default clock-sync checks.
//
//nolint:gochecknoglobals // operational default; tests override and restore it.
var DefaultClockSyncProviders = []string{
	"time.apple.com",
	"time.cloudflare.com",
	"pool.ntp.org",
}

var sntpOffsetRE = regexp.MustCompile(`(?m)^\s*([+-]?[0-9]+(?:\.[0-9]+)?)\s*(?:\+/-|$)`)

func parseSNTPDrift(trimmed string) (time.Duration, error) {
	m := sntpOffsetRE.FindStringSubmatch(trimmed)
	if len(m) < 2 {
		return 0, fmt.Errorf("%w: sntp %q", ErrClockProbeUnexpectedOutput, trimmed)
	}
	seconds, parseErr := strconv.ParseFloat(m[1], 64)
	if parseErr != nil {
		return 0, fmt.Errorf("%w: sntp %q", ErrClockProbeUnexpectedOutput, trimmed)
	}
	return time.Duration(seconds * float64(time.Second)), nil
}
