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
	"log/slog"
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
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Demo 1: pre-task freshness gate.
	if cfg.socket != "" {
		if err := freshnessGate(ctx, logger, cfg.socket); err != nil {
			logger.ErrorContext(ctx, "freshness gate failed", slog.Any("err", err))
			os.Exit(1)
		}
	} else {
		logger.InfoContext(ctx, "skip freshness gate", slog.String("reason", "no --socket / HUSH_STATUS_SOCKET"))
	}

	// Demo 2: capability discovery (optional; requires server + key).
	if cfg.serverURL != "" && cfg.clientKeyPEM != "" {
		if err := capabilityDiscovery(ctx, logger, cfg); err != nil {
			logger.WarnContext(ctx, "capability discovery failed", slog.Any("err", err))
		}
	} else {
		logger.InfoContext(ctx, "skip capability discovery", slog.String("reason", "supply --server and --client-key-pem to enable"))
	}

	// Demo 3: lifecycle watcher (long-running; the example runs for 10s).
	if cfg.socket != "" {
		runCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		lifecycleWatch(runCtx, logger, cfg.socket, cfg.watchInterval)
	}

	logger.InfoContext(ctx, "done")
}

// freshnessGate refuses to start work when any scope is stale.
func freshnessGate(ctx context.Context, logger *slog.Logger, socket string) error {
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
	logger.InfoContext(ctx, "freshness gate ok",
		slog.Int("scope_healthy_count", len(status.ScopeHealthy)),
		slog.String("session_expires_at", status.SessionExpiresAt.Format(time.RFC3339)))
	return nil
}

// capabilityDiscovery asks the vault server what scopes exist and
// what the current session looks like — no Discord approval.
func capabilityDiscovery(ctx context.Context, logger *slog.Logger, cfg config) error {
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
	logger.InfoContext(ctx, "me",
		slog.String("server_version", resp.ServerVersion),
		slog.Int("schema_version", resp.SchemaVersion),
		slog.Any("scopes_available", resp.ScopesAvailable))
	if resp.CurrentSession != nil {
		logger.InfoContext(ctx, "current session",
			slog.String("jti", resp.CurrentSession.JTI),
			slog.String("expires_at", resp.CurrentSession.ExpiresAt.Format(time.RFC3339)),
			slog.Any("scopes", resp.CurrentSession.Scopes),
			slog.Int("max_uses", resp.CurrentSession.MaxUses))
	} else {
		logger.InfoContext(ctx, "no current_session (no bearer or bearer invalid)")
	}
	return nil
}

// lifecycleWatch demonstrates reactive event handling: state changes,
// session renewals, and pre-expiry warnings flow through one channel.
// A cooperative agent uses ExpiresSoon to checkpoint and shut down
// cleanly BEFORE its credentials rotate.
func lifecycleWatch(ctx context.Context, logger *slog.Logger, socket string, pollInterval time.Duration) {
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
		logger.WarnContext(ctx, "watch start failed", slog.Any("err", err))
		return
	}
	logger.InfoContext(ctx, "watch subscribed; listening for events")
	for ev := range events {
		switch ev.Type {
		case client.EventInitial:
			logger.InfoContext(ctx, "watch initial",
				slog.String("state", string(ev.Status.State)),
				slog.String("expires_at", ev.Status.SessionExpiresAt.Format(time.RFC3339)))
		case client.EventStateChange:
			logger.InfoContext(ctx, "watch state-change", slog.String("state", string(ev.Status.State)))
		case client.EventScopeHealthChange:
			logger.InfoContext(ctx, "watch scope-change",
				slog.Any("healthy", ev.Status.ScopeHealthy),
				slog.Any("stale", ev.Status.ScopeStale))
		case client.EventSessionRenewed:
			logger.InfoContext(ctx, "watch session-renewed", slog.String("jti", ev.Status.SessionJTI))
		case client.EventExpiresSoon:
			logger.WarnContext(ctx, "watch expires-soon — checkpoint / wind down NOW",
				slog.Duration("threshold", ev.Threshold))
		case client.EventError:
			logger.WarnContext(ctx, "watch transient error (continuing)", slog.Any("err", ev.Err))
		}
	}
	logger.InfoContext(ctx, "watch channel closed (ctx done)")
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
