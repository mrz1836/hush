// Package vault is the on-disk secret store: an AES-256-GCM-encrypted file
// containing JSON-encoded name → SecureBytes mappings, written via temp-
// file + fsync + atomic rename, with strict permission enforcement at both
// open and save paths.
//
// # File format
//
// On-disk layout: 4-byte magic ("HUSH") + 1-byte version (0x01) + 16-byte
// salt + 12-byte AES-GCM nonce + ciphertext (= AES-256-GCM-Seal(plaintext,
// derivedKey, nonce)). The plaintext is canonical JSON of the in-memory
// shape produced by [codec]; keys (vault encryption key) are derived
// from the supplied [*securebytes.SecureBytes] passphrase + salt and
// scrubbed when [Save]/[Load] returns.
//
// # Atomic write
//
// [Save] writes a temp file in the parent directory (mode 0600), fsyncs
// it, renames over the target, then chmods to neutralize umask. On any
// failure the temp file is unconditionally removed. The parent directory
// must be 0700 or [Save] refuses.
//
// # Permissions
//
// The vault file must be 0600 and its parent dir 0700 when
// [SecuritySection.RequireFileModeChecks] is true (the production
// default). Looser permissions surface as [ErrFilePermsLoose].
//
// Constitutional principles in scope: I (no plaintext keys on disk;
// vault key is derived per call), II (file format spec is locked), VIII
// (fuzz harness over the parser), IX (context-first APIs, no init, no
// globals beyond sentinels), X (errors carry no secret bytes).
//
// # Exported entry points
//
//   - [Load] — read + decrypt + permission-check; returns an in-memory [Store].
//   - [Save] — encrypt + atomic-write + permission-set.
//   - [Store] — interface for the in-memory secret repository.
//   - [Secret] — single (name, value, description) record.
//   - Sentinels: [ErrBadMagic], [ErrBadVersion], [ErrShortHeader],
//     [ErrAuthFailed], [ErrFilePermsLoose], [ErrSecretNotFound],
//     [ErrStoreDestroyed], [ErrDuplicateName], [ErrFileTooLarge],
//     [ErrInvalidName].
//
// # Usage sketch
//
//	vaultKey, _ := securebytes.New(derivedKey)
//	defer vaultKey.Destroy()
//
//	store, err := vault.Load(ctx, "/path/to/vault", vaultKey)
//	if err != nil { return err }
//	defer store.Destroy()
//
//	sb, err := store.Get("OPENAI_API_KEY")
//	// ... use via sb.Use(...)
//
// The in-memory [Store] is safe for concurrent use; [Save] is not
// safe to interleave with concurrent [Get]/Use on the same store.
package vault
