package server

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/mrz1836/hush/internal/vault"
	"github.com/mrz1836/hush/internal/vault/securebytes"
)

// ReloadVault loads a new vault from newPath using the supplied vault key,
// atomically swaps the in-memory pointer, and destroys the previous store
// after the configured drain window.
//
// Calls are serialised: a second call blocks until the first completes its
// swap and destroy. Returns a typed sentinel error wrapped via fmt.Errorf
// ([ErrReloadFileMissing], [ErrReloadDecryptFailed], [ErrReloadInvalid]) on
// failure; the active vault pointer is unchanged on any error. During
// shutdown, returns [ErrShuttingDown].
//
// The chassis's SIGHUP handler invokes ReloadVault internally using the
// configured vault path and the [Deps.VaultKey] supplied at construction.
func (s *Server) ReloadVault(ctx context.Context, newPath string, key *securebytes.SecureBytes) error {
	return s.runReload(ctx, newPath, key)
}

// runReload is the reload coordinator. It holds [Server.reloadMu] across the
// entire load → swap → drain → destroy cycle so FR-014 ("the new reload does
// not begin until the previous reload's swap and destroy have completed") is
// honoured literally.
//
//nolint:cyclop // sequential reload state machine: lock → load → swap → audit → drain → destroy; complexity is structural
func (s *Server) runReload(ctx context.Context, newPath string, key *securebytes.SecureBytes) error {
	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()

	if s.shuttingDown.Load() {
		return ErrShuttingDown
	}
	if key == nil {
		return fmt.Errorf("server: reload: nil vault key: %w", ErrReloadInvalid)
	}

	newStore, err := s.loadVault(ctx, newPath, key)
	if err != nil {
		return wrapReloadError(newPath, err)
	}
	if newStore == nil {
		return fmt.Errorf("server: reload: nil store for %q: %w", newPath, ErrReloadInvalid)
	}

	oldStorePtr := s.vaultPtr.Swap(&newStore)

	if writeErr := s.audit.Write(ctx, AuditEvent{
		Type: AuditVaultReloaded,
		At:   s.clock(),
		Detail: map[string]string{
			"to_path": newPath,
		},
	}); writeErr != nil {
		s.logger.WarnContext(ctx, "audit write vault_reloaded failed", "err", writeErr.Error())
	}

	s.drainAndDestroy(ctx, oldStorePtr)
	return nil
}

// drainAndDestroy waits for the configured drain window (or for the
// shutdown deadline channel to close, whichever fires first) then destroys
// the previous vault store. Idempotent destruction is delegated to
// [vault.Store.Destroy].
func (s *Server) drainAndDestroy(ctx context.Context, oldStorePtr *vault.Store) {
	if oldStorePtr == nil {
		return
	}
	s.drainWG.Add(1)
	defer s.drainWG.Done()

	timer := time.NewTimer(s.reloadDrainWindow)
	defer timer.Stop()

	select {
	case <-timer.C:
	case <-s.shutdownDoneCh:
	case <-ctx.Done():
	}
	if err := (*oldStorePtr).Destroy(); err != nil {
		s.logger.ErrorContext(ctx, "vault destroy after drain", "err", err.Error())
	}
}

// wrapReloadError categorises an underlying vault load error into one of the
// three reload sentinels and wraps it with the failing path. The message is
// path-bearing but never includes any byte from the vault file's ciphertext
// or plaintext (FR-013).
func wrapReloadError(path string, err error) error {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("server: reload: vault %q missing: %w", path, ErrReloadFileMissing)
	case errors.Is(err, vault.ErrAuthFailed):
		return fmt.Errorf("server: reload: vault %q authentication failed: %w", path, ErrReloadDecryptFailed)
	case errors.Is(err, vault.ErrFilePermsLoose),
		errors.Is(err, vault.ErrBadMagic),
		errors.Is(err, vault.ErrBadVersion),
		errors.Is(err, vault.ErrShortHeader),
		errors.Is(err, vault.ErrFileTooLarge),
		errors.Is(err, vault.ErrInvalidName),
		errors.Is(err, vault.ErrDuplicateName):
		return fmt.Errorf("server: reload: vault %q invalid: %w", path, ErrReloadInvalid)
	default:
		return fmt.Errorf("server: reload: vault %q load failed: %w", path, ErrReloadInvalid)
	}
}
