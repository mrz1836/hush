//go:build never_built

// Package contracts captures the locked Go signatures that
// SDD-21 commits internal/supervise to. This file is for review
// only — the canonical implementation lives at
// internal/supervise/{refill,refresh,grace}.go after Phase 5
// (/speckit-implement). Reviewers should diff this against the
// implemented surface and flag any drift.
//
// Build-tagged with `never_built` so lint/build skip it; this
// directory has no go.mod and is excluded from the repo's Go module.
// The file exists solely as a typed mirror of the API contract.
package contracts

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/mrz1836/hush/internal/vault/securebytes"
)

// ----- Sentinel errors (locked, exported) -----

// ErrJTIUnknown is returned (wrapped) by Refill when the vault server
// returns HTTP 401 with body {"error":"unknown_jti"}. The orchestrator
// (SDD-23) MUST emit EventFetchAuthRequired (SDD-19) to transition the
// supervisor to StateAwaitingApproval (FR-021-3).
var ErrJTIUnknown = errors.New("supervise: vault rejected JWT (unknown jti)")

// ErrBootTimeout is the sentinel the orchestrator's boot-retry helper
// (SDD-23) returns when the boot_retry_timeout budget is exhausted
// without a successful health probe (FR-021-20). Declared in SDD-21
// because the locked PACKAGE-MAP entry lists it; this chunk does NOT
// produce it from any code path (R-010).
var ErrBootTimeout = errors.New("supervise: boot retry timeout exhausted")

// ----- Refiller -----

// Refiller fetches and decrypts the per-supervisor scope set from
// the vault server. One Refiller is wired per supervisor by the
// orchestrator (SDD-23); refill cycles are serialized through the
// supervisor state machine — Refill is NOT safe for concurrent
// invocation against the same instance.
type Refiller struct {
	client *http.Client
	store  *Store
	grace  *Grace
	priv   *ecdsa.PrivateKey
	logger *slog.Logger
	server string
}

// NewRefiller constructs a Refiller bound to the supplied dependencies.
// Panics if any argument is nil (Constitution IX startup-wiring exemption).
//
// Locked exported signature per docs/sdd/SDD-21.md. Additional dependencies
// (Grace handle, ECIES private key, server URL prefix) are wired through
// a package-private (*Refiller).attach method invoked by the orchestrator.
func NewRefiller(client *http.Client, store *Store, logger *slog.Logger) *Refiller {
	return &Refiller{client: client, store: store, logger: logger}
}

// Refill fetches every name in scopes from the vault server using the
// JWT held in store.Snapshot().Token. On success, every decrypted
// *SecureBytes is handed to grace.Set(name, sb) and Refill returns nil.
// On any error, every successfully decrypted *SecureBytes from the
// current call is destroyed BEFORE Refill returns (FR-021-5).
//
// Returned errors:
//   - errors.Is(err, ErrJTIUnknown): server returned 401 with body
//     {"error":"unknown_jti"} (FR-021-3). Orchestrator MUST transition
//     to StateAwaitingApproval.
//   - any other non-nil error: wrapped underlying error from net / DNS /
//     timeout / non-401 HTTP / decode / ECIES (FR-021-4). Orchestrator
//     decides retry policy.
//   - ctx.Err() (wrapped): caller cancelled.
//
// Refill MUST NOT retry internally (caller-managed boot retry per
// chunk doc). Refill MUST NOT log any decrypted secret value
// (Constitution X).
func (r *Refiller) Refill(ctx context.Context, scopes []string) error {
	_ = ctx
	_ = scopes
	return nil
}

// ----- Refresher -----

// Refresher schedules at most one refill callback fire per configured
// local-time window per calendar day, plus at most one T-30 fallback
// fire per session (FR-021-7 / FR-021-8). Run blocks until ctx is
// cancelled. Single-shot — Run returns sentinel error on second call.
type Refresher struct {
	window string
	ttl    time.Duration
	refill func(ctx context.Context) error
	logger *slog.Logger

	now          func() time.Time
	bornAt       time.Time
	lastFiredDay time.Time
	t30Fired     bool

	runOnce sync.Once
	ran     bool
}

// NewRefresher constructs a Refresher bound to the supplied window
// string, session TTL, fire callback, and logger. Panics if window
// fails to parse, if refill is nil, or if logger is nil (Constitution
// IX startup-wiring exemption).
//
// Locked exported signature per docs/sdd/SDD-21.md. The window string
// MUST be canonical "HH:MM-HH:MM" (validated by SDD-18's parser at
// config-load time); the orchestrator (SDD-23) passes the already-
// validated string verbatim.
func NewRefresher(window string, ttl time.Duration, refill func(ctx context.Context) error, logger *slog.Logger) *Refresher {
	return &Refresher{window: window, ttl: ttl, refill: refill, logger: logger}
}

// Run drives the scheduler tick loop. Returns ctx.Err() on
// cancellation; never any other error from a normal run. A second
// call to Run on the same *Refresher returns a sentinel error
// immediately (sync.Once-guarded). Spawns NO goroutines beyond its
// own tick loop body.
//
// On entry, if the wall clock is already inside the configured window
// AND lastFiredDay != today, Run fires once on init (FR-021-10).
//
// On a non-nil error from refill, Run logs WARN naming the error
// category and advances lastFiredDay anyway — the fire counts as
// "issued" per FR-021-11a (rate-limited refresh fires never retry
// inside the same window).
func (r *Refresher) Run(ctx context.Context) error {
	_ = ctx
	return nil
}

// ----- Grace -----

// Grace is the per-supervisor cache of last-decrypted *SecureBytes
// keyed by secret name. Lifecycle: NewGrace returns an empty cache;
// Refiller.Refill calls Set after each successful decrypt cycle; the
// orchestrator's restart path calls Get; the `hush client refresh`
// flow calls Evict. The cache is permanently empty when enabled=false
// or window=0 (FR-021-14). Effective TTL is min(window, 4h) (FR-021-12).
type Grace struct {
	mu      sync.RWMutex
	entries map[string]graceEntry
	enabled bool
	window  time.Duration
	now     func() time.Time
}

type graceEntry struct {
	sb      *securebytes.SecureBytes
	expires time.Time
}

// NewGrace constructs a Grace cache. window is hard-capped at 4 hours
// (FR-021-12). Disabled mode (enabled=false) and zero-window
// (window<=0) both produce a permanently-empty cache (FR-021-14).
//
// NewGrace owns no goroutines (Constitution IX, R-008): expired
// entries are destroyed lazily on the next Get call rather than via
// a sweeper.
//
// Locked exported signature per docs/sdd/SDD-21.md.
func NewGrace(window time.Duration, enabled bool) *Grace {
	cap4h := 4 * time.Hour
	if window > cap4h {
		window = cap4h
	}
	return &Grace{
		entries: make(map[string]graceEntry),
		enabled: enabled,
		window:  window,
		now:     time.Now,
	}
}

// Get returns the cached *SecureBytes for name. Returns (nil, false)
// when the entry is absent, expired, or when the cache is disabled.
// On expiry, Get atomically destroys the entry's *SecureBytes and
// removes the map slot before returning (R-008 lazy-evict).
//
// The returned *SecureBytes pointer is borrow-only — callers MUST NOT
// call Destroy on it. Grace retains ownership until the next Set,
// Evict, or expiry-on-Get.
func (g *Grace) Get(name string) (*securebytes.SecureBytes, bool) {
	_ = name
	return nil, false
}

// Set records (name, value) with expiry = now() + window. On
// overwrite, the prior entry's *SecureBytes is destroyed first
// (FR-021-13). When the cache is disabled or window<=0, Set is a
// silent no-op and ownership of value remains with the caller (R-009).
func (g *Grace) Set(name string, value *securebytes.SecureBytes) {
	_ = name
	_ = value
}

// Evict destroys the entry for name (if present) and removes the
// map slot. Calling Evict for an absent name is a silent no-op
// (Clarification 5 / FR-021-16).
func (g *Grace) Evict(name string) {
	_ = name
}

// ----- Borrowed type stubs (for compile-only mirror) -----

// Store mirrors internal/supervise/state.go's Store; this declaration
// exists only so the file compiles against the contracts directory in
// isolation. The real Store lives in package supervise (SDD-19).
type Store struct{}

// Snapshot mirrors internal/supervise/state.go's Snapshot.
type Snapshot struct {
	Token *securebytes.SecureBytes
}

// Snapshot returns a stub snapshot.
func (s *Store) Snapshot() Snapshot { return Snapshot{} }
