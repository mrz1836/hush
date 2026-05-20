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
// (SDD-23) MUST emit EventFetchAuthRequired (SDD-19) to transition the
// supervisor to StateAwaitingApproval (FR-021-3). Compare via
// errors.Is(err, supervise.ErrJTIUnknown).
var ErrJTIUnknown = errors.New("supervise: vault rejected JWT (unknown jti)")

// ErrBootTimeout is the sentinel the orchestrator's boot-retry helper
// (SDD-23) returns when the boot_retry_timeout budget is exhausted.
// Declared in this chunk because the locked SDD-21 exported API lists
// it; this chunk does NOT produce it from any code path (R-010,
// FR-021-20).
var ErrBootTimeout = errors.New("supervise: boot retry timeout exhausted")

// errNilBearerToken is wrapped by Refill when the cached JWT is
// missing — a programmer-error escape hatch (RR-7).
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
// payload (RR-5).
const refillBodyCap = 64 * 1024

// Refiller fetches and decrypts the per-supervisor scope set from
// the vault server. One Refiller is wired per supervisor by the
// orchestrator (SDD-23) at boot; refill cycles are serialized through
// the supervisor state machine — Refill is NOT safe for concurrent
// invocation against the same instance.
//
// The locked SDD-21 constructor signature accepts only client/store/
// logger. Three additional dependencies (Grace handle, ECIES private
// key, server URL prefix) are wired post-construction by the
// orchestrator via the package-private (*Refiller).attach method.
type Refiller struct {
	client *http.Client
	store  *Store
	grace  *Grace
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
// the JWT held in store.Snapshot().Token. On success, every decrypted
// *SecureBytes is handed to grace.Set(name, sb) and Refill returns
// nil. On any error, every successfully decrypted *SecureBytes from
// the current call is destroyed BEFORE Refill returns (FR-021-5).
//
// Returned errors:
//   - errors.Is(err, ErrJTIUnknown): the server returned 401 with
//     body {"error":"unknown_jti"} (FR-021-3). Orchestrator MUST
//     transition to StateAwaitingApproval.
//   - any other non-nil error: a wrapped underlying error from the
//     network / DNS / TLS / non-401 HTTP / JSON decode / ECIES
//     decrypt path (FR-021-4).
//
// Refill MUST NOT retry internally — caller (SDD-23) owns the retry
// loop. Refill MUST NOT log decrypted secret values (Constitution X);
// the SOLE permitted string(...) materialization in this method is
// the JWT bearer-header path inside Snapshot.Token.Use (FR-021-15).
func (r *Refiller) Refill(ctx context.Context, scopes []string) error {
	committed := false
	decrypted := make([]struct {
		name string
		sb   *securebytes.SecureBytes
	}, 0, len(scopes))
	defer func() {
		if committed {
			return
		}
		for i := range decrypted {
			_ = decrypted[i].sb.Destroy()
		}
	}()

	snap := r.store.Snapshot()
	if snap.Token == nil {
		return errNilBearerToken
	}

	for _, name := range scopes {
		sb, err := r.fetchOne(ctx, name, snap.Token)
		if err != nil {
			r.logger.Info("refill: scope failed",
				slog.String("scope", name),
				slog.String("outcome", classifyOutcome(err)),
				slog.Any("err", err),
			)
			return err
		}
		decrypted = append(decrypted, struct {
			name string
			sb   *securebytes.SecureBytes
		}{name: name, sb: sb})
		r.logger.Info("refill: scope ok",
			slog.String("scope", name),
			slog.String("outcome", "ok"),
		)
	}

	for i := range decrypted {
		r.grace.Set(decrypted[i].name, decrypted[i].sb)
	}
	committed = true
	return nil
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
		// FR-021-15: JWT bearer-header materialization, scoped to
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
func (r *Refiller) attach(grace *Grace, priv *ecdsa.PrivateKey, serverURL string) {
	r.grace = grace
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
// operational logger (FR-021-6). The label never embeds secret bytes.
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
