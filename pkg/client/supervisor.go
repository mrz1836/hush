package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

// SupervisorStatus is a client for a single supervisor's Unix-domain
// status socket. Construct via NewSupervisorStatus; the zero value is
// not usable.
//
// All methods are safe for concurrent use by multiple goroutines —
// each call opens a fresh socket connection. There is no persistent
// state to share.
type SupervisorStatus struct {
	socketPath string
}

// NewSupervisorStatus returns a client bound to socketPath. The path
// is not validated until the first round-trip; passing an empty or
// non-existent path is permitted and surfaces as ErrSocketUnavailable
// on first use.
func NewSupervisorStatus(socketPath string) *SupervisorStatus {
	return &SupervisorStatus{socketPath: socketPath}
}

// SocketPath returns the absolute path this client was constructed
// with. Useful for diagnostics.
func (s *SupervisorStatus) SocketPath() string {
	return s.socketPath
}

// Close releases any background resources. Currently a no-op because
// each call opens its own connection; reserved for future event-stream
// wiring where a long-lived subscription connection needs explicit
// teardown.
func (s *SupervisorStatus) Close() error { return nil }

// SnapshotRaw dials the status socket, sends the "status" verb, and
// returns the raw JSON response bytes terminated by exactly one
// newline. Use this when forwarding the document to a downstream JSON
// consumer that should observe any new fields the SDK does not yet
// know about.
func (s *SupervisorStatus) SnapshotRaw(ctx context.Context) ([]byte, error) {
	body, err := s.roundTrip(ctx, "status\n")
	if err != nil {
		return nil, err
	}
	return ensureSingleTrailingNewline(body), nil
}

// Snapshot dials the status socket and returns the parsed Status
// document. Wire-format additions appear as new omitempty fields and
// older SDK builds silently drop them — call SnapshotRaw to preserve
// them.
func (s *SupervisorStatus) Snapshot(ctx context.Context) (*Status, error) {
	body, err := s.roundTrip(ctx, "status\n")
	if err != nil {
		return nil, err
	}
	var doc statusWire
	if jerr := json.Unmarshal(bytes.TrimSpace(body), &doc); jerr != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidResponse, jerr)
	}
	return doc.toStatus()
}

// Refresh asks the supervisor to perform an immediate refill — that
// is, re-fetch its scopes from the vault and gracefully restart the
// supervised child. Returns nil when the supervisor reports success.
// Returns an error wrapping ErrRefreshDenied (with the supervisor's
// reason string) when the supervisor declined the request.
func (s *SupervisorStatus) Refresh(ctx context.Context) error {
	body, err := s.roundTrip(ctx, "refresh\n")
	if err != nil {
		return err
	}
	var ack refreshAckWire
	if jerr := json.Unmarshal(bytes.TrimSpace(body), &ack); jerr != nil {
		return fmt.Errorf("%w: %w", ErrInvalidResponse, jerr)
	}
	if ack.OK {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrRefreshDenied, ack.Error)
}

// roundTrip dials the socket, sends verb, reads to EOF or context
// deadline, and returns the bytes. Single attempt; never retries.
// Any dial / write / read failure wraps ErrSocketUnavailable.
func (s *SupervisorStatus) roundTrip(ctx context.Context, verb string) ([]byte, error) {
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", s.socketPath)
	if err != nil {
		return nil, fmt.Errorf("%w: dial %s: %w", ErrSocketUnavailable, s.socketPath, err)
	}
	defer func() { _ = conn.Close() }()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if _, werr := conn.Write([]byte(verb)); werr != nil {
		return nil, fmt.Errorf("%w: write verb: %w", ErrSocketUnavailable, werr)
	}
	body, rerr := io.ReadAll(conn)
	if rerr != nil && !errors.Is(rerr, io.EOF) {
		return nil, fmt.Errorf("%w: read response: %w", ErrSocketUnavailable, rerr)
	}
	return body, nil
}

// statusWire is the on-the-wire DTO mirroring supervise.statusJSON.
// Kept private so the public Status type can evolve independently.
type statusWire struct {
	Supervisor        string   `json:"supervisor"`
	SessionExpiresAt  string   `json:"session_expires_at"`
	SessionJTI        string   `json:"session_jti"`
	RestartCount      uint64   `json:"restart_count"`
	RefreshWindowNext string   `json:"refresh_window_next"`
	ScopeHealthy      []string `json:"scope_healthy"`
	ScopeStale        []string `json:"scope_stale"`
	LastAuthFailure   *string  `json:"last_auth_failure"`
	ChildPID          *int     `json:"child_pid"`
	ChildUptime       string   `json:"child_uptime"`
	DiscordConnected  bool     `json:"discord_connected"`
	State             string   `json:"state"`
}

func (w *statusWire) toStatus() (*Status, error) {
	exp, err := parseRFC3339OrZero(w.SessionExpiresAt)
	if err != nil {
		return nil, fmt.Errorf("%w: session_expires_at: %w", ErrInvalidResponse, err)
	}
	next, err := parseRFC3339OrZero(w.RefreshWindowNext)
	if err != nil {
		return nil, fmt.Errorf("%w: refresh_window_next: %w", ErrInvalidResponse, err)
	}
	var lastFail time.Time
	if w.LastAuthFailure != nil {
		lastFail, err = parseRFC3339OrZero(*w.LastAuthFailure)
		if err != nil {
			return nil, fmt.Errorf("%w: last_auth_failure: %w", ErrInvalidResponse, err)
		}
	}
	uptime := time.Duration(0)
	if w.ChildUptime != "" {
		uptime, err = time.ParseDuration(w.ChildUptime)
		if err != nil {
			return nil, fmt.Errorf("%w: child_uptime: %w", ErrInvalidResponse, err)
		}
	}
	pid := 0
	if w.ChildPID != nil {
		pid = *w.ChildPID
	}
	return &Status{
		Supervisor:        w.Supervisor,
		State:             State(w.State),
		SessionJTI:        w.SessionJTI,
		SessionExpiresAt:  exp,
		RestartCount:      w.RestartCount,
		RefreshWindowNext: next,
		ScopeHealthy:      w.ScopeHealthy,
		ScopeStale:        w.ScopeStale,
		LastAuthFailure:   lastFail,
		ChildPID:          pid,
		ChildUptime:       uptime,
		DiscordConnected:  w.DiscordConnected,
	}, nil
}

// refreshAckWire mirrors the supervise refresh-ack DTO.
type refreshAckWire struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// parseRFC3339OrZero accepts either an RFC3339 string or an empty
// string (returning zero time). The supervisor always emits a string
// (zero time formats as "0001-01-01T00:00:00Z"), so the empty branch
// is defensive only.
func parseRFC3339OrZero(s string) (time.Time, error) {
	if strings.TrimSpace(s) == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, err
	}
	return t, nil
}

// ensureSingleTrailingNewline returns body with exactly one trailing
// '\n' — adds one when absent, trims duplicates when present.
func ensureSingleTrailingNewline(body []byte) []byte {
	body = bytes.TrimRight(body, "\n")
	body = append(body, '\n')
	return body
}
