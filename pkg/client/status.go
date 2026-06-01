package client

import "time"

// State enumerates the supervisor lifecycle states exposed on the
// status socket. The string values match the wire encoding so callers
// can compare directly against the JSON field.
type State string

// Status is the typed projection of the supervisor's status document.
// All time fields use zero values to signal absence rather than
// separate "set" booleans:
//
//   - SessionExpiresAt / RefreshWindowNext / ResealNext / LastAuthFailure: zero
//     time means "not applicable" (no session yet, no refresh window
//     configured, no reseal schedule, never failed).
//   - ChildPID == 0 means the supervisor has no child running.
//   - ChildUptime == 0 means no child or child just started.
type Status struct {
	Supervisor        string
	State             State
	SessionJTI        string
	SessionExpiresAt  time.Time
	RestartCount      uint64
	RefreshWindowNext time.Time
	ResealNext        time.Time
	ScopeHealthy      []string
	ScopeStale        []string
	LastAuthFailure   time.Time
	ChildPID          int
	ChildUptime       time.Duration
	DiscordConnected  bool
}
