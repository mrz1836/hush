// SDD-22 Unix-domain status socket: filesystem-perms-as-auth listener
// emitting the FR-12 status JSON document on every accepted connection.
package supervise

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ErrAlreadyRunning is returned by (*StatusServer).Run on a second
// invocation of the same instance — concurrent or sequential. Re-binding
// requires a fresh StatusServer (FR-022-14a). Compare via
// errors.Is(err, supervise.ErrAlreadyRunning).
var ErrAlreadyRunning = errors.New("supervise: status server already running")

// errParentNotDir is returned (wrapped) by ensureParentMode0700 when the
// configured parent path exists but is not a directory. Package-private —
// programmer-error class; orchestrator surfaces it via the wrapped chain.
var errParentNotDir = errors.New("supervise: parent path is not a directory")

// StatusInputs is the consumer-defined seam for FR-12 fields not held by
// SDD-19's Snapshot. Implementations MUST be safe for concurrent reads —
// the status server may invoke any getter from any handler goroutine.
// Wired post-construction via the package-private (*StatusServer).attach.
// Pre-attach (the server's inputs field is nil), the document renders zero
// values for these fields.
type StatusInputs interface {
	Name() string
	SessionExpiresAt() time.Time
	RefreshWindowNext() time.Time
	ScopeHealthy() []string
	ScopeStale() []string
	LastAuthFailure() *time.Time
	ChildUptime() time.Duration
	DiscordConnected() bool
}

// StatusServer is a Unix-domain status listener. Construct via
// NewStatusServer; drive via Run(ctx). Single-shot Run per instance:
// re-binding after a lifecycle stop requires a fresh StatusServer.
type StatusServer struct {
	socketPath string
	store      *Store
	logger     *slog.Logger

	mu      sync.Mutex
	inputs  StatusInputs
	started bool
	conns   map[net.Conn]struct{}
	wg      sync.WaitGroup
}

// NewStatusServer constructs a fresh StatusServer. Pure value constructor
// — performs ZERO syscalls. Panics if logger is nil. store may be nil for
// unit-test flexibility; production callers MUST supply a non-nil *Store.
func NewStatusServer(socketPath string, store *Store, logger *slog.Logger) *StatusServer {
	if logger == nil {
		panic("supervise: NewStatusServer requires a non-nil *slog.Logger")
	}
	return &StatusServer{
		socketPath: socketPath,
		store:      store,
		logger:     logger,
		conns:      make(map[net.Conn]struct{}),
	}
}

// Run binds the listener at the configured socketPath and serves status
// requests until ctx is cancelled. Returns nil on clean ctx-cancelled
// shutdown after every spawned goroutine has joined. Returns
// ErrAlreadyRunning on second invocation, ErrSocketPermsLoose when the
// parent directory mode is laxer than 0700, or any other I/O error
// wrapped with %w.
func (s *StatusServer) Run(ctx context.Context) error {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return fmt.Errorf("supervise: %w", ErrAlreadyRunning)
	}
	s.started = true
	s.mu.Unlock()

	parent := filepath.Dir(s.socketPath)
	if err := ensureParentMode0700(parent); err != nil {
		return err
	}
	if err := os.Remove(s.socketPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("supervise: status socket cleanup: %w", err)
	}
	var lc net.ListenConfig
	listener, err := lc.Listen(ctx, "unix", s.socketPath)
	if err != nil {
		return fmt.Errorf("supervise: status socket listen: %w", err)
	}
	if err := os.Chmod(s.socketPath, 0o600); err != nil {
		_ = listener.Close()
		return fmt.Errorf("supervise: status socket chmod: %w", err)
	}

	done := make(chan struct{})
	s.wg.Add(1)
	go s.watch(ctx, listener, done)

	s.acceptLoop(listener)

	close(done)
	s.wg.Wait()
	return nil
}

// attach wires inputs into the status server. Package-private; called by
// the orchestrator (SDD-23) from inside package supervise. Mirrors
// SDD-21's (*Refiller).attach precedent.
func (s *StatusServer) attach(inputs StatusInputs) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inputs = inputs
}

// watch is the cancellation goroutine. Owner: Run. Cancellation:
// <-ctx.Done() OR <-done. Termination: returns after closing the listener
// and force-closing every tracked in-flight conn.
func (s *StatusServer) watch(ctx context.Context, listener net.Listener, done chan struct{}) {
	defer s.wg.Done()
	defer func() {
		if r := recover(); r != nil {
			s.logger.Error("supervise: status watcher panic", "recover", r)
		}
	}()
	select {
	case <-ctx.Done():
	case <-done:
		return
	}
	_ = listener.Close()
	s.mu.Lock()
	for c := range s.conns {
		_ = c.Close()
	}
	s.mu.Unlock()
}

// acceptLoop runs in Run's frame (no extra goroutine). On each accepted
// conn, registers it under s.mu and spawns a per-connection handler. Exits
// when Accept returns net.ErrClosed (listener.Close from watcher).
func (s *StatusServer) acceptLoop(listener net.Listener) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			s.logger.Warn("supervise: status accept error", "err", err)
			continue
		}
		s.mu.Lock()
		s.conns[conn] = struct{}{}
		s.mu.Unlock()
		s.wg.Add(1)
		go s.handle(conn)
	}
}

// handle is the per-connection goroutine. Owner: acceptLoop. Cancellation:
// watcher's conn.Close() propagates as Read/Write error. Termination:
// handler returns; wg.Done().
func (s *StatusServer) handle(conn net.Conn) {
	defer s.wg.Done()
	defer func() {
		s.mu.Lock()
		delete(s.conns, conn)
		s.mu.Unlock()
		_ = conn.Close()
	}()
	defer func() {
		if r := recover(); r != nil {
			s.logger.Error("supervise: status handler panic", "recover", r)
		}
	}()

	br := bufio.NewReader(conn)
	if _, err := br.ReadString('\n'); err != nil {
		// Tolerate unterminated request — render the document anyway.
		// FR-022 §2.5: "request payload is advisory in v0.1.0; the connection IS the auth."
		if !errors.Is(err, net.ErrClosed) {
			s.logger.Debug("supervise: status request read error", "err", err)
		}
	}

	body, err := s.renderStatus(s.snapshotForResponse())
	if err != nil {
		s.logger.Error("supervise: status encode error", "err", err)
		return
	}
	body = append(body, '\n')
	if _, err := conn.Write(body); err != nil {
		if !errors.Is(err, net.ErrClosed) {
			s.logger.Debug("supervise: status write error", "err", err)
		}
	}
}

// snapshotForResponse takes ONE Store.Snapshot() per request (FR-022-16).
// Returns the zero Snapshot when store is nil (unit-testing flexibility).
func (s *StatusServer) snapshotForResponse() Snapshot {
	if s.store == nil {
		return Snapshot{}
	}
	return s.store.Snapshot()
}

// statusJSON is the FR-12 wire DTO. Snapshot.Token is intentionally NOT a
// field — token bytes never reach the wire (Constitution X / FR-022-13).
type statusJSON struct {
	Supervisor        string   `json:"supervisor"`
	SessionExpiresAt  string   `json:"session_expires_at"`
	RefreshWindowNext string   `json:"refresh_window_next"`
	ScopeHealthy      []string `json:"scope_healthy"`
	ScopeStale        []string `json:"scope_stale"`
	LastAuthFailure   *string  `json:"last_auth_failure"`
	ChildPID          *int     `json:"child_pid"`
	ChildUptime       string   `json:"child_uptime"`
	DiscordConnected  bool     `json:"discord_connected"`
	State             string   `json:"state"`
}

// renderStatus projects one Snapshot + one inputs read into the FR-12
// JSON document. Zero values render shape-conformant when inputs is nil.
func (s *StatusServer) renderStatus(snap Snapshot) ([]byte, error) {
	s.mu.Lock()
	inputs := s.inputs
	s.mu.Unlock()

	doc := statusJSON{
		ScopeHealthy: []string{},
		ScopeStale:   []string{},
		ChildUptime:  time.Duration(0).String(),
		State:        string(snap.State),
	}
	if snap.ChildPID > 0 {
		pid := snap.ChildPID
		doc.ChildPID = &pid
	}
	doc.SessionExpiresAt = time.Time{}.Format(time.RFC3339)
	doc.RefreshWindowNext = time.Time{}.Format(time.RFC3339)

	if inputs != nil {
		doc.Supervisor = inputs.Name()
		doc.SessionExpiresAt = inputs.SessionExpiresAt().Format(time.RFC3339)
		doc.RefreshWindowNext = inputs.RefreshWindowNext().Format(time.RFC3339)
		if h := inputs.ScopeHealthy(); h != nil {
			doc.ScopeHealthy = h
		}
		if st := inputs.ScopeStale(); st != nil {
			doc.ScopeStale = st
		}
		if laf := inputs.LastAuthFailure(); laf != nil {
			s := laf.Format(time.RFC3339)
			doc.LastAuthFailure = &s
		}
		doc.ChildUptime = inputs.ChildUptime().String()
		doc.DiscordConnected = inputs.DiscordConnected()
	}

	return json.Marshal(doc)
}

// ensureParentMode0700 is consumed by both AcquirePidFile and
// (*StatusServer).Run per research.md R-4. Returns ErrSocketPermsLoose
// (wrapped) when the parent exists but its mode is laxer than 0700.
// Creates the parent at 0700 when missing. Any other I/O error is
// returned wrapped (distinguishable from ErrSocketPermsLoose via errors.Is).
func ensureParentMode0700(parent string) error {
	info, err := os.Stat(parent)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			if mkErr := os.MkdirAll(parent, 0o700); mkErr != nil {
				return fmt.Errorf("supervise: parent mkdir: %w", mkErr)
			}
			return nil
		}
		return fmt.Errorf("supervise: parent stat: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%w (path=%s)", errParentNotDir, parent)
	}
	if info.Mode().Perm() != 0o700 {
		return fmt.Errorf("supervise: %w (path=%s mode=%v)", ErrSocketPermsLoose, parent, info.Mode().Perm())
	}
	return nil
}
