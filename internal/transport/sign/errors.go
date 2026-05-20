package sign

import "errors"

var (
	// ErrSignatureInvalid is returned by [Verify] for every signature failure:
	// wrong key, tampered payload, malformed DER, wrong-length sig.
	ErrSignatureInvalid = errors.New("hush/transport/sign: signature invalid")

	// ErrNonceReplay is returned by [NonceCache.Add] when the nonce was already
	// accepted within its TTL.
	ErrNonceReplay = errors.New("hush/transport/sign: nonce replay")

	// ErrNonceEncoding is returned by [NonceCache.Add] when the nonce length is
	// outside [8, 128].
	ErrNonceEncoding = errors.New("hush/transport/sign: nonce encoding invalid (length out of [8,128])")

	// ErrNonceTTLInvalid is returned by [NonceCache.Add] when ttl <= 0.
	ErrNonceTTLInvalid = errors.New("hush/transport/sign: nonce ttl must be positive")

	// ErrTimestampStale is exposed so consumers can errors.Is-match it; the
	// package itself does not return it — [IsFreshTimestamp] returns bool and
	// consumers map false to this sentinel.
	ErrTimestampStale = errors.New("hush/transport/sign: timestamp outside freshness window")

	// ErrCanonicalUnsupported is returned by [CanonicalJSON] for non-finite
	// floats, non-string-keyed maps, and unsupported Go kinds.
	ErrCanonicalUnsupported = errors.New("hush/transport/sign: value cannot be canonicalised")
)
