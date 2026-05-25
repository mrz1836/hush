package supervise

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/mrz1836/hush/internal/transport/ecies"
	"github.com/mrz1836/hush/internal/vault/securebytes"
)

// ErrJTIUnknown is returned (wrapped) by Refill when the vault server
// responds HTTP 401 with body {"error":"unknown_jti"}. The orchestrator
// MUST emit EventFetchAuthRequired to transition the supervisor to
// StateAwaitingApproval. Compare via
// errors.Is(err, supervise.ErrJTIUnknown).
var ErrJTIUnknown = errors.New("supervise: vault rejected JWT (unknown jti)")

// ErrBootTimeout is the sentinel the orchestrator's boot-retry helper
// returns when the boot_retry_timeout budget is exhausted. Declared
// here for the exported API; this file does NOT produce it from any
// code path.
var ErrBootTimeout = errors.New("supervise: boot retry timeout exhausted")

// errNilBearerToken is wrapped by Refill when the cached JWT is
// missing — a programmer-error escape hatch.
var errNilBearerToken = errors.New("supervise/refill: nil bearer token in store")

// errStatusUnauthorizedUnparseable, errStatusUnauthorizedOther, and
// errStatusUnexpected back the err113-compliant non-JTI HTTP error
// shapes returned by fetchOne.
var (
	errStatusUnauthorizedUnparseable = errors.New("supervise/refill: status=401 unparseable body")
	errStatusUnauthorizedOther       = errors.New("supervise/refill: status=401 non-jti error")
	errStatusUnexpected              = errors.New("supervise/refill: unexpected status")
)

// refillBodyCap caps the size of any vault response body Refill will
// read into memory. The vault server emits BIE1 ECIES envelopes whose
// upper bound is bounded by the underlying secret length plus a fixed
// envelope overhead; 64 KiB is well above any plausible per-name
// payload.
const refillBodyCap = 64 * 1024

// Refiller fetches and decrypts the per-supervisor scope set from
// the vault server. One Refiller is wired per supervisor by the
// orchestrator at boot; refill cycles are serialized through the
// supervisor state machine — Refill is NOT safe for concurrent
// invocation against the same instance.
//
// The constructor signature accepts only client/store/logger. Two
// additional dependencies (ECIES private key, server URL prefix) are
// wired post-construction by the orchestrator via the package-private
// (*Refiller).attach method. Refill returns the freshly decrypted
// secrets to the caller; the caller — not the Refiller — owns the
// retention decision (Grace cache) and the lifetime of the returned
// *SecureBytes.
type Refiller struct {
	client *http.Client
	store  *Store
	priv   *ecdsa.PrivateKey
	logger *slog.Logger
	server string
}

// NewRefiller constructs a Refiller bound to the supplied dependencies.
// Panics if client, store, or logger is nil (Constitution IX startup-
// wiring exemption).
func NewRefiller(client *http.Client, store *Store, logger *slog.Logger) *Refiller {
	if client == nil {
		panic("supervise: NewRefiller requires a non-nil *http.Client")
	}
	if store == nil {
		panic("supervise: NewRefiller requires a non-nil *Store")
	}
	if logger == nil {
		panic("supervise: NewRefiller requires a non-nil *slog.Logger")
	}
	return &Refiller{client: client, store: store, logger: logger}
}

// Refill fetches every name in scopes from the vault server using
// the JWT held in store.Snapshot().Token. On success it returns a map
// of scope name → freshly decrypted *SecureBytes; ownership of every
// returned *SecureBytes transfers to the caller. On any error, every
// successfully decrypted *SecureBytes from the current call is
// destroyed BEFORE Refill returns and the map is nil.
//
// The bearer-token snapshot is re-captured before each fetchOne so a
// concurrent Store.setToken from claimRefreshLoop (which destroys the
// prior *SecureBytes eagerly per Principle VI) does not surface as a
// phantom ErrDestroyed mid-Refill. Per-scope snapshotting narrows the
// rotation race to the window between Snapshot and the next tok.Use,
// well inside a single fetchOne. Both old and new JWTs are server-
// valid, so mixing across scopes inside one Refill is benign.
//
// Returned errors:
//   - errors.Is(err, ErrJTIUnknown): the server returned 401 with
//     body {"error":"unknown_jti"}. Orchestrator MUST transition to
//     StateAwaitingApproval.
//   - any other non-nil error: a wrapped underlying error from the
//     network / DNS / TLS / non-401 HTTP / JSON decode / ECIES
//     decrypt path.
//
// Refill MUST NOT retry internally — the caller owns the retry loop.
// Refill MUST NOT log decrypted secret values; the SOLE permitted
// string(...) materialization in this method is the JWT bearer-header
// path inside Snapshot.Token.Use.
func (r *Refiller) Refill(ctx context.Context, scopes []string) (map[string]*securebytes.SecureBytes, error) {
	committed := false
	out := make(map[string]*securebytes.SecureBytes, len(scopes))
	defer func() {
		if committed {
			return
		}
		for name := range out {
			_ = out[name].Destroy()
		}
	}()

	for _, name := range scopes {
		snap := r.store.Snapshot()
		if snap.Token == nil {
			return nil, errNilBearerToken
		}
		sb, err := r.fetchOne(ctx, name, snap.Token)
		if err != nil {
			r.logger.Info(
				"refill: scope failed",
				slog.String("scope", name),
				slog.String("outcome", classifyOutcome(err)),
				slog.Any("err", err),
			)
			return nil, err
		}
		out[name] = sb
		r.logger.Info(
			"refill: scope ok",
			slog.String("scope", name),
			slog.String("outcome", "ok"),
		)
	}

	committed = true
	return out, nil
}

// fetchOne issues a single GET <server>/s/<name> call and returns the
// decrypted *SecureBytes on success. The caller is responsible for
// destruction-on-error via the committed-bool pattern in Refill.
func (r *Refiller) fetchOne(ctx context.Context, name string, tok *securebytes.SecureBytes) (*securebytes.SecureBytes, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.server+"/s/"+name, nil)
	if err != nil {
		return nil, fmt.Errorf("supervise/refill: build request: %w", err)
	}

	if usErr := tok.Use(func(b []byte) {
		// JWT bearer-header materialization, scoped to
		// Snapshot.Token.Use closure (sole permitted string(...)
		// site, applies to JWT not vault payload).
		req.Header.Set("Authorization", "Bearer "+string(b))
	}); usErr != nil {
		return nil, fmt.Errorf("supervise/refill: read bearer: %w", usErr)
	}

	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("supervise/refill: transport: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, refillBodyCap))
	if err != nil {
		return nil, fmt.Errorf("supervise/refill: read body: %w", err)
	}

	if statusErr := classifyStatus(resp.StatusCode, body); statusErr != nil {
		return nil, statusErr
	}

	sb, err := ecies.Decrypt(ctx, r.priv, body)
	for i := range body {
		body[i] = 0
	}
	if err != nil {
		return nil, fmt.Errorf("supervise/refill: decrypt: %w", err)
	}
	return sb, nil
}

// attach is a package-private wiring method invoked once by the
// orchestrator after construction. It preserves the 3-arg
// NewRefiller signature while giving the orchestrator a way to
// inject post-init dependencies.
func (r *Refiller) attach(priv *ecdsa.PrivateKey, serverURL string) {
	r.priv = priv
	r.server = serverURL
}

// classifyStatus maps an HTTP response (status + body) to either a
// non-nil error indicating the request failed, or nil to signal that
// the body should be ECIES-decrypted as a successful payload.
func classifyStatus(status int, body []byte) error {
	switch status {
	case http.StatusOK:
		return nil
	case http.StatusUnauthorized:
		var parsed struct {
			Error string `json:"error"`
		}
		if jerr := json.Unmarshal(body, &parsed); jerr != nil {
			return errStatusUnauthorizedUnparseable
		}
		if parsed.Error == "unknown_jti" {
			return fmt.Errorf("supervise/refill: %w", ErrJTIUnknown)
		}
		return fmt.Errorf("%w: %q", errStatusUnauthorizedOther, parsed.Error)
	default:
		return fmt.Errorf("%w: status=%d", errStatusUnexpected, status)
	}
}

// classifyOutcome maps an error to a coarse outcome label for the
// operational logger. The label never embeds secret bytes.
func classifyOutcome(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, ErrJTIUnknown):
		return "jti-unknown"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "cancelled"
	default:
		return "transient"
	}
}
