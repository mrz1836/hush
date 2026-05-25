// Package main is a worked example of an AI agent integrating with
// hush via the public pkg/client SDK.
//
// It demonstrates the three core SDK surfaces:
//
//  1. SupervisorStatus.Snapshot — a pre-task freshness gate so the
//     agent refuses to start work when scopes are stale.
//  2. SupervisorStatus.Watch — a reactive event channel that lets
//     the agent wind down gracefully BEFORE its credentials expire.
//  3. Me — a signed (no Discord buzz) capability-discovery call
//     that tells the agent what scopes the vault holds and what its
//     current session looks like.
//
// The example is intentionally minimal — no real work, just logging.
// Treat it as a template: copy the structure into your own agent and
// replace runWork() with whatever your tool actually does.
//
// To run:
//
//	# inside a `hush supervise` child (HUSH_STATUS_SOCKET is exported):
//	go run ./examples/agent
//
// To exercise Me() standalone (outside a supervisor child), supply
// --server, --client-key-pem, --machine-name flags. See parseFlags.
package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mrz1836/hush/pkg/client"
)

// Sentinel errors. err113 wants wrapped statics, not ad-hoc strings.
var (
	errStaleScopes = errors.New("refusing to run — stale scopes")
	errNoPEMBlock  = errors.New("no PEM block found in client-key file")
)

type config struct {
	socket        string
	serverURL     string
	clientKeyPEM  string
	machineName   string
	bearerJWT     string
	watchInterval time.Duration
}

func parseFlags() config {
	c := config{}
	flag.StringVar(&c.socket, "socket", os.Getenv("HUSH_STATUS_SOCKET"),
		"Absolute path to the supervisor's status socket (defaults to HUSH_STATUS_SOCKET)")
	flag.StringVar(&c.serverURL, "server", "",
		"Vault server URL including the random prefix (optional; required to call /me)")
	flag.StringVar(&c.clientKeyPEM, "client-key-pem", "",
		"Path to a PEM-encoded EC private key for signing /me requests (optional)")
	flag.StringVar(&c.machineName, "machine-name", mustHostname(),
		"Machine name to send on /me requests")
	flag.StringVar(&c.bearerJWT, "bearer", os.Getenv("HUSH_BEARER"),
		"Optional Bearer JWT for /me to populate CurrentSession (defaults to HUSH_BEARER)")
	flag.DurationVar(&c.watchInterval, "watch-interval", 30*time.Second,
		"Poll interval for Watch() events")
	flag.Parse()
	return c
}

func main() {
	cfg := parseFlags()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Demo 1: pre-task freshness gate.
	if cfg.socket != "" {
		if err := freshnessGate(ctx, cfg.socket); err != nil {
			log.Fatalf("freshness gate failed: %v", err)
		}
	} else {
		log.Println("[skip] freshness gate — no --socket / HUSH_STATUS_SOCKET")
	}

	// Demo 2: capability discovery (optional; requires server + key).
	if cfg.serverURL != "" && cfg.clientKeyPEM != "" {
		if err := capabilityDiscovery(ctx, cfg); err != nil {
			log.Printf("capability discovery failed: %v", err)
		}
	} else {
		log.Println("[skip] capability discovery — supply --server and --client-key-pem to enable")
	}

	// Demo 3: lifecycle watcher (long-running; the example runs for 10s).
	if cfg.socket != "" {
		runCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		lifecycleWatch(runCtx, cfg.socket, cfg.watchInterval)
	}

	log.Println("done.")
}

// freshnessGate refuses to start work when any scope is stale.
func freshnessGate(ctx context.Context, socket string) error {
	sup := client.NewSupervisorStatus(socket)
	defer func() { _ = sup.Close() }()

	gateCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	status, err := sup.Snapshot(gateCtx)
	if err != nil {
		return fmt.Errorf("snapshot: %w", err)
	}
	if len(status.ScopeStale) > 0 {
		return fmt.Errorf("%w: %v", errStaleScopes, status.ScopeStale)
	}
	log.Printf("[gate] scopes healthy (%d); session expires at %s",
		len(status.ScopeHealthy), status.SessionExpiresAt.Format(time.RFC3339))
	return nil
}

// capabilityDiscovery asks the vault server what scopes exist and
// what the current session looks like — no Discord approval.
func capabilityDiscovery(ctx context.Context, cfg config) error {
	key, err := loadECPrivateKeyPEM(cfg.clientKeyPEM)
	if err != nil {
		return fmt.Errorf("load client key: %w", err)
	}
	resp, err := client.Me(ctx, client.MeRequest{
		ServerURL:   cfg.serverURL,
		ClientKey:   key,
		BearerJWT:   cfg.bearerJWT,
		MachineName: cfg.machineName,
	})
	if err != nil {
		if errors.Is(err, client.ErrUnauthenticated) {
			return fmt.Errorf("server rejected signed request — is this machine enrolled? %w", err)
		}
		return err
	}
	log.Printf("[me] server v%s schema v%d; scopes available: %v",
		resp.ServerVersion, resp.SchemaVersion, resp.ScopesAvailable)
	if resp.CurrentSession != nil {
		log.Printf("[me] current session: jti=%s expires=%s scopes=%v max_uses=%d",
			resp.CurrentSession.JTI,
			resp.CurrentSession.ExpiresAt.Format(time.RFC3339),
			resp.CurrentSession.Scopes,
			resp.CurrentSession.MaxUses)
	} else {
		log.Println("[me] no current_session (no bearer or bearer invalid)")
	}
	return nil
}

// lifecycleWatch demonstrates reactive event handling: state changes,
// session renewals, and pre-expiry warnings flow through one channel.
// A cooperative agent uses ExpiresSoon to checkpoint and shut down
// cleanly BEFORE its credentials rotate.
func lifecycleWatch(ctx context.Context, socket string, pollInterval time.Duration) {
	sup := client.NewSupervisorStatus(socket)
	defer func() { _ = sup.Close() }()

	events, err := sup.Watch(ctx, client.WatchOptions{
		PollInterval: pollInterval,
		ExpiryThresholds: []time.Duration{
			15 * time.Minute,
			5 * time.Minute,
			time.Minute,
		},
	})
	if err != nil {
		log.Printf("[watch] start failed: %v", err)
		return
	}
	log.Println("[watch] subscribed; listening for events…")
	for ev := range events {
		switch ev.Type {
		case client.EventInitial:
			log.Printf("[watch] initial — state=%s expires=%s",
				ev.Status.State, ev.Status.SessionExpiresAt.Format(time.RFC3339))
		case client.EventStateChange:
			log.Printf("[watch] state-change → %s", ev.Status.State)
		case client.EventScopeHealthChange:
			log.Printf("[watch] scope-change healthy=%v stale=%v",
				ev.Status.ScopeHealthy, ev.Status.ScopeStale)
		case client.EventSessionRenewed:
			log.Printf("[watch] session-renewed jti=%s", ev.Status.SessionJTI)
		case client.EventExpiresSoon:
			log.Printf("[watch] expires-soon (≤%s) — checkpoint / wind down NOW", ev.Threshold)
		case client.EventError:
			log.Printf("[watch] transient error: %v (continuing)", ev.Err)
		}
	}
	log.Println("[watch] channel closed (ctx done)")
}

// loadECPrivateKeyPEM reads a PEM-encoded EC private key from disk.
// Production agents should NOT load the key from disk — hush's
// invariant is "no key files anywhere." Use the keychain APIs in
// internal/keychain instead. This is example-only convenience.
func loadECPrivateKeyPEM(path string) (*ecdsa.PrivateKey, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // example program; path comes from operator flag
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, errNoPEMBlock
	}
	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	return key, nil
}

func mustHostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return h
}
