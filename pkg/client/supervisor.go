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

// ReloadResult is the success outcome of (*SupervisorStatus).Reload.
// All fields are non-secret kernel/wall-clock identifiers. Strategy is
// the wire-stable handoff strategy string — "http-proxy" in v1.
type ReloadResult struct {
	OldPID            int
	NewPID            int
	ReadinessDuration time.Duration
	Strategy          string
}

// Reload asks the supervisor to perform a zero-downtime HTTP-proxy
// handoff against its currently-loaded config. configPath is the path
// the operator validated locally before triggering the reload; the
// supervisor uses its already-loaded config for the actual swap, but
// the path is forwarded so the supervisor's audit log records which
// file the operator associated the request with.
//
// Returns the populated ReloadResult and nil on success. On failure,
// the error wraps one of:
//
//   - ErrReloadConfigInvalid — the supervisor's config is not
//     reload-eligible (missing [child.readiness] or [child.handoff]
//     mode = "http-proxy", or proxy listener not attached).
//   - ErrReloadReadinessFailed — replacement child failed the HTTP
//     readiness probe; old child is still serving.
//   - ErrReloadInFlight — another reload is already running.
//   - ErrReloadFailed — any other supervisor-side failure.
//   - ErrSocketUnavailable — the supervisor socket could not be
//     reached (maps to the "supervisor-unreachable" CLI result code).
//   - ErrInvalidResponse — the supervisor responded but the payload
//     could not be parsed (version skew or corruption).
//
// Compare with errors.Is.
func (s *SupervisorStatus) Reload(ctx context.Context, configPath string) (ReloadResult, error) {
	reqBody, mErr := json.Marshal(reloadReqWire{ConfigPath: configPath})
	if mErr != nil {
		return ReloadResult{}, fmt.Errorf("%w: marshal reload request: %w", ErrInvalidResponse, mErr)
	}
	verb := "reload " + string(reqBody) + "\n"
	body, err := s.roundTrip(ctx, verb)
	if err != nil {
		return ReloadResult{}, err
	}
	var ack reloadAckWire
	if jerr := json.Unmarshal(bytes.TrimSpace(body), &ack); jerr != nil {
		return ReloadResult{}, fmt.Errorf("%w: %w", ErrInvalidResponse, jerr)
	}
	if ack.OK {
		return ReloadResult{
			OldPID:            ack.OldPID,
			NewPID:            ack.NewPID,
			ReadinessDuration: time.Duration(ack.ReadinessDurationMS) * time.Millisecond,
			Strategy:          ack.Strategy,
		}, nil
	}
	return ReloadResult{}, classifyReloadAck(ack)
}

// classifyReloadAck maps a failure ack onto the typed reload error.
// Unknown result strings fall through to ErrReloadFailed so the
// caller still receives a non-nil error with the supervisor's
// reason string.
func classifyReloadAck(ack reloadAckWire) error {
	reason := ack.Error
	if reason == "" {
		reason = ack.Result
	}
	switch ack.Result {
	case reloadResultConfigInvalid:
		return fmt.Errorf("%w: %s", ErrReloadConfigInvalid, reason)
	case reloadResultReadinessFailed:
		return fmt.Errorf("%w: %s", ErrReloadReadinessFailed, reason)
	case reloadResultSwapInFlight:
		return fmt.Errorf("%w: %s", ErrReloadInFlight, reason)
	}
	return fmt.Errorf("%w: %s", ErrReloadFailed, reason)
}

// supervisorDefaultTimeout caps a single status-socket round-trip
// when the caller's context carries no deadline. 5s is generous for
// a same-host Unix-socket call while still bounding a runaway peer.
const supervisorDefaultTimeout = 5 * time.Second

// supervisorMaxResponseBytes caps a single status-socket response.
// Matches the server's MaxRequestBodyBytes (64 KiB at
// internal/server/server.go). The supervisor's status JSON is well
// under 1 KiB; ack payloads are smaller still.
const supervisorMaxResponseBytes = 64 << 10

// roundTrip dials the socket, sends verb, reads to EOF or context
// deadline, and returns the bytes. Single attempt; never retries.
// Any dial / write / read failure wraps ErrSocketUnavailable.
// Responses larger than supervisorMaxResponseBytes are rejected with
// ErrInvalidResponse rather than silently truncated.
func (s *SupervisorStatus) roundTrip(ctx context.Context, verb string) ([]byte, error) {
	ctx, cancel := ensureDeadline(ctx, supervisorDefaultTimeout)
	defer cancel()
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
	body, rerr := io.ReadAll(io.LimitReader(conn, supervisorMaxResponseBytes+1))
	if rerr != nil && !errors.Is(rerr, io.EOF) {
		return nil, fmt.Errorf("%w: read response: %w", ErrSocketUnavailable, rerr)
	}
	if len(body) > supervisorMaxResponseBytes {
		return nil, fmt.Errorf("%w: supervisor response exceeded %d bytes", ErrInvalidResponse, supervisorMaxResponseBytes)
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

// reloadReqWire mirrors supervise.ReloadRequest — the JSON body that
// follows the `reload` verb on the request line.
type reloadReqWire struct {
	ConfigPath string `json:"config_path"`
}

// reloadAckWire mirrors supervise.reloadAckWire — the unified
// success/failure response shape for the `reload` verb.
type reloadAckWire struct {
	OK                  bool   `json:"ok"`
	Result              string `json:"result"`
	OldPID              int    `json:"old_pid,omitempty"`
	NewPID              int    `json:"new_pid,omitempty"`
	ReadinessDurationMS int64  `json:"readiness_ms,omitempty"`
	Strategy            string `json:"strategy,omitempty"`
	Error               string `json:"error,omitempty"`
	ConfigPath          string `json:"config_path,omitempty"`
}

// Reload result code constants (mirrored from the server's
// wire-stable strings). Kept package-private because callers compare
// against the typed sentinels (ErrReloadConfigInvalid, ...) rather
// than the raw codes.
const (
	reloadResultConfigInvalid   = "config-invalid"
	reloadResultReadinessFailed = "readiness-failed"
	reloadResultSwapInFlight    = "swap-in-flight"
)

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
