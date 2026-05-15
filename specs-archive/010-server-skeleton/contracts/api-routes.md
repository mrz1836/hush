# Contract — API Routes

The chassis owns the route surface; SDD-12 and SDD-13 attach handler
bodies. This contract documents the surface SDD-10 locks.

---

## Mount surface

All chassis-served routes live under `/h/<prefix>/...` where
`<prefix>` is `Cfg.Server.PathPrefix` (validated by `internal/config`
to match `[A-Za-z0-9_-]{6,32}`).

The chassis registers the mount on a single `*http.ServeMux`
created once during `Run`. SDD-12 and SDD-13 register their handlers
via the chassis-exposed registration hook:

```go
// (*Server).Mount registers a handler under the chassis's mounted prefix.
// Method must be one of POST, GET. Path must begin with "/" and must NOT
// repeat the "/h/<prefix>" prefix — Mount prepends it.
//
// Mount is safe to call only before Run; calls after Run starts the listener
// return ErrAlreadyRun.
func (s *Server) Mount(method, path string, h http.Handler) error
```

Examples (filled in by SDD-12, SDD-13):

```go
// SDD-12 (claim handler):
srv.Mount("POST", "/claim", claimHandler)
// → effective: POST /h/<prefix>/claim

// SDD-13 (secrets, revoke, health):
srv.Mount("GET",  "/s/{name}",     secretHandler)
srv.Mount("POST", "/revoke/{jti}", revokeHandler)
srv.Mount("GET",  "/hz",           healthHandler)
```

`Mount` records the (method, path, handler) tuple; the actual
`mux.Handle` call happens during `Run` after startup checks pass.
This ordering guarantees no route is registered against a server
that fails to start.

---

## Route table (locked from `docs/API.md`)

| Method | Path | Owner chunk |
|--------|------|-------------|
| `POST` | `/h/<prefix>/claim` | SDD-12 |
| `GET`  | `/h/<prefix>/s/{name}` | SDD-13 |
| `POST` | `/h/<prefix>/revoke/{jti}` | SDD-13 |
| `GET`  | `/h/<prefix>/hz` | SDD-13 |

Any path NOT in this table returns `404 Not Found`. The chassis
does not register any catch-all handler — `http.ServeMux`'s
default 404 is sufficient.

---

## Middleware order (locked)

Every request entering the mux passes through, in order:

1. **Request ID middleware** — assigns 16 random bytes from
   `crypto/rand` (hex-encoded) to the request context. Ignores
   any client-supplied header (FR-016, FR-017).
2. **IP allow-list middleware** — compares socket-level peer
   address against `Cfg.Network.AllowedCIDRs`; rejects with
   `403 Forbidden` and an audit-log entry before any handler runs
   (FR-018).
3. **Body cap (`http.MaxBytesReader`)** — wraps `r.Body` to cap
   reads at 64 KiB; bodies over the cap return `413 Payload Too
   Large` when the handler attempts to read.
4. **Panic recover middleware** — captures panics from handlers
   and middleware below; logs panic + stack + request_id; never
   logs request body; returns `500 Internal Server Error`
   (FR-019, FR-020).
5. **Handler** — registered by SDD-12 / SDD-13.

The order is a chassis invariant. Tests assert each property
independently and assert the *combined* property: a request from
a non-allow-listed IP is rejected before the panic-recover layer
runs (FR-026).

---

## Health endpoint exception

`GET /h/<prefix>/hz` is implemented by SDD-13. The chassis does not
short-circuit it through the middleware stack — the same chain
runs (request ID + allow-list + body cap + panic recover) — so a
probe from an unallowed IP is rejected with 403 even on the health
endpoint. This is intentional: health probes are operational and
must come from inside the Tailscale mesh.

`Cfg.Network.HealthBind` exists as an extension point (separate
listener). For v0.1.0 the chassis attaches `/hz` to the same mux
on the same listener as everything else; the field is reserved
but not used until a follow-up SDD chunk.

---

## Status codes used by the chassis itself

| Code | When | Body |
|------|------|------|
| `403 Forbidden` | IP allow-list rejection | `"forbidden\n"` |
| `404 Not Found` | unknown path | `http.ServeMux` default |
| `405 Method Not Allowed` | wrong method on known path | `http.ServeMux` default (Go ≥ 1.22 method-aware) |
| `413 Payload Too Large` | body exceeds 64 KiB | `http.MaxBytesReader` default |
| `500 Internal Server Error` | recovered panic | `"internal server error\n"` |

The chassis never returns `401`, `503`, or `200` — those are
handler-owned codes. The chassis never returns a body containing
secret material, request-body bytes, panic detail, or stack trace.
