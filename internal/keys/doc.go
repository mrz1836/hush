// Package keys derives every cryptographic key used by hush at runtime
// from a single passphrase, with no key material ever written to disk.
//
// The chain is:
//
//	passphrase + 16-byte salt → Argon2id (time=4, memory=256 MiB, threads=4,
//	keyLen=64) → master seed → BIP32 hardened-only HD derivation → typed
//	per-purpose keys (JWT signing, vault encryption, audit signing, per-
//	machine client signing).
//
// Constitutional principles in scope: I (key files banned on disk),
// III (Argon2id parameter floors), V (BIP32 hardened paths preventing
// public-key attacks), IX (context-first APIs, no init, no globals beyond
// sentinels), X (errors carry no key material).
//
// # Threat model
//
// The package assumes its inputs (passphrase, salt) are trustworthy at
// derivation time and that the output keys are zeroed by callers when no
// longer needed. It does not attempt to defend against in-process memory
// scraping during the brief window the seed is in scope; pair with
// internal/vault/securebytes for that property.
//
// # Exported entry points
//
//   - [DeriveMasterSeed] — Argon2id KDF over passphrase + salt; returns 64 B.
//   - [DeriveJWTSigningKey] — BIP32 path m/44'/7743'/0'/0/0 → secp256k1 priv.
//   - [DeriveVaultEncKey] — BIP32 path m/44'/7743'/1'/0/0 → 32 B AES-256 key.
//   - [DeriveAuditSigningKey] — BIP32 path m/44'/7743'/2'/0/0 → secp256k1 priv.
//   - [DeriveClientKey] — BIP32 path m/44'/7743'/3'/0/<machineIndex>.
//   - [PublicKeyFingerprint] — first-8-byte SHA-256 of compressed pub.
//   - [ErrPassphraseTooShort], [ErrSaltMissing] — sentinels for input rejects.
//
// # Usage sketch
//
//	seed, err := keys.DeriveMasterSeed(ctx, []byte(passphrase), salt)
//	if err != nil { return err }
//	defer clear(seed)
//
//	jwtKey, _ := keys.DeriveJWTSigningKey(seed)
//	vaultKey, _ := keys.DeriveVaultEncKey(seed)
//
// Post-entry context cancellation is intentionally NOT honored by
// DeriveMasterSeed: Argon2id is CPU-bound and cannot be safely interrupted.
package keys
