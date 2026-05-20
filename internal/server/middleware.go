package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"runtime/debug"
	"strings"
)

// randReadFn is the chassis's bridge to a 16-byte CSPRNG read used by the
// request-ID middleware. Replaceable in tests so the rand-failure path is
// covered without invoking [crypto/rand]'s fatal error in Go 1.26+.
//
//nolint:gochecknoglobals // OS bridge; test-hookable for rand-failure coverage
var randReadFn io.Reader = rand.Reader

// requestIDMiddleware assigns a fresh, server-generated request ID to every
// inbound request. Reads 16 bytes from crypto/rand, hex-encodes them
// (32 chars), and stores the result in the request context under the
// package-private [requestIDKey]. Client-supplied X-Request-ID and similar
// headers are ignored unconditionally — the chassis is the sole source of
// truth for request IDs.
func (s *Server) requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 16)
		if _, err := io.ReadFull(randReadFn, buf); err != nil {
			s.logger.ErrorContext(r.Context(), "request id generation failed", "err", err.Error())
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		id := hex.EncodeToString(buf)
		ctx := context.WithValue(r.Context(), requestIDKey, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// ipAllowListMiddleware compares the socket-level peer address of every
// request against the parsed allow-list of CIDRs. On mismatch, writes
// 403 Forbidden, emits an [AuditAuthFailedNotAllowed] event, and returns
// without invoking next. The check ignores X-Forwarded-For and similar
// headers — only the connection's RemoteAddr is consulted.
func (s *Server) ipAllowListMiddleware(allowed []netip.Prefix) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			peer, ok := parseRemoteAddr(r.RemoteAddr)
			if !ok || !allowedByCIDR(peer, allowed) {
				s.rejectNotAllowed(w, r, peer)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// rejectNotAllowed writes the 403 response and emits the audit event. Pulled
// out of [Server.ipAllowListMiddleware] to keep the per-request handler
// short and inline-friendly.
func (s *Server) rejectNotAllowed(w http.ResponseWriter, r *http.Request, peer netip.Addr) {
	id := RequestID(r.Context())
	if writeErr := s.audit.Write(r.Context(), AuditEvent{
		Type:      AuditAuthFailedNotAllowed,
		At:        s.clock(),
		RequestID: id,
		ClientIP:  peer,
		Detail: map[string]string{
			"reason": "not_in_allowed_cidrs",
		},
	}); writeErr != nil {
		s.logger.WarnContext(r.Context(), "audit write auth_failed", "err", writeErr.Error())
	}
	http.Error(w, "forbidden", http.StatusForbidden)
}

// parseRemoteAddr extracts a [netip.Addr] from the typical
// host:port string carried in [http.Request.RemoteAddr].
func parseRemoteAddr(remoteAddr string) (netip.Addr, bool) {
	if ap, err := netip.ParseAddrPort(remoteAddr); err == nil {
		return ap.Addr().Unmap(), true
	}
	if a, err := netip.ParseAddr(remoteAddr); err == nil {
		return a.Unmap(), true
	}
	if i := strings.LastIndex(remoteAddr, ":"); i > 0 {
		if a, err := netip.ParseAddr(remoteAddr[:i]); err == nil {
			return a.Unmap(), true
		}
	}
	return netip.Addr{}, false
}

// allowedByCIDR reports whether peer is contained by any of the configured
// allow-list prefixes.
func allowedByCIDR(peer netip.Addr, allowed []netip.Prefix) bool {
	if !peer.IsValid() {
		return false
	}
	for _, p := range allowed {
		if p.Contains(peer) {
			return true
		}
	}
	return false
}

// bodyCapMiddleware wraps [http.Request.Body] with [http.MaxBytesReader] so a
// hostile peer cannot exhaust memory by sending a multi-megabyte body. The
// cap is the chassis-wide [MaxRequestBodyBytes].
func (s *Server) bodyCapMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, MaxRequestBodyBytes)
		next.ServeHTTP(w, r)
	})
}

// recoverMiddleware is the chassis's panic-recover layer. It logs the panic
// value, the stack trace, and the request_id at ERROR — but never any byte
// of the request body. Returns 500 Internal Server Error with a generic body.
//
// A second-level panic (a panic from inside the recover handler — e.g. a
// logger that itself panics) is caught by an inner deferred recover; for
// that single request the chassis logs only the request_id and returns
// without leaking detail. The connection may be cut by [http.Server], but
// the process stays alive and unrelated requests are unaffected.
func (s *Server) recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			s.recoverFromHandlerPanic(w, r, recover())
		}()
		next.ServeHTTP(w, r)
	})
}

// recoverFromHandlerPanic does the work of [Server.recoverMiddleware]. It is
// safe to call with a nil panic value — that branch is the no-panic path.
func (s *Server) recoverFromHandlerPanic(w http.ResponseWriter, r *http.Request, panicVal any) {
	if panicVal == nil {
		return
	}
	// Inner deferred recover catches any second-level panic raised by the
	// logger, audit writer, or http.Error helper below. The handler is
	// deliberately silent — anything that itself logs would risk a third
	// panic. The outer http.Server still tracks per-connection state, so
	// this single request fails closed without taking the server down.
	defer func() { _ = recover() }()

	id := RequestID(r.Context())
	stack := string(debug.Stack())
	s.logger.ErrorContext(r.Context(), "handler panic",
		"panic", fmt.Sprintf("%v", panicVal),
		"stack", stack,
		"request_id", id,
	)
	if writeErr := s.audit.Write(r.Context(), AuditEvent{
		Type:      AuditPanicCaptured,
		At:        s.clock(),
		RequestID: id,
		Detail: map[string]string{
			"panic": fmt.Sprintf("%v", panicVal),
		},
	}); writeErr != nil {
		s.logger.WarnContext(r.Context(), "audit write panic_captured failed", "err", writeErr.Error())
	}
	http.Error(w, "internal server error", http.StatusInternalServerError)
}

// middlewareChain assembles the locked middleware order:
// request ID → IP allow-list → body cap → panic recover → handler.
//
// Outermost (executed first) is the request-ID assignment; innermost (just
// outside the handler) is the panic-recover. The panic-recover layer
// always wraps the handler — it is unconditional. When the IP allow-list
// rejects a request the handler is never invoked, so no panic can
// originate from it; the 403 path simply returns before recover has any
// work to do. This is a structural guarantee, not a conditional bypass
// of the chain.
func (s *Server) middlewareChain(handler http.Handler) http.Handler {
	allowed := parseAllowedCIDRs(s.cfg.Network.AllowedCIDRs)

	chain := s.recoverMiddleware(handler)
	chain = s.bodyCapMiddleware(chain)
	chain = s.ipAllowListMiddleware(allowed)(chain)
	return s.requestIDMiddleware(chain)
}

// parseAllowedCIDRs converts the configured CIDR strings (validated at TOML
// time) to [netip.Prefix] values. Any parse failure here is silently
// dropped because [internal/config] has already rejected malformed CIDRs.
func parseAllowedCIDRs(raw []string) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(raw))
	for _, s := range raw {
		if p, err := netip.ParsePrefix(s); err == nil {
			out = append(out, p)
		}
	}
	return out
}
