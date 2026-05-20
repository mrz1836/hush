package server

import (
	"fmt"
	"net/http"
	"strings"
)

// Mount registers a handler under the chassis's mounted prefix.
//
// method must be one of POST, GET, PUT, DELETE, PATCH, HEAD, OPTIONS. path
// must begin with "/" and must NOT repeat the "/h/<prefix>" prefix — Mount
// prepends it from [Cfg.Server.PathPrefix].
//
// Mount is safe to call only before [Server.Run]. Calls after Run starts the
// listener return [ErrAlreadyRun]. The chassis records each (method, path,
// handler) tuple and applies them to the underlying mux during Run after
// startup checks pass — this guarantees no route is registered against a
// server that fails to start.
func (s *Server) Mount(method, path string, h http.Handler) error {
	if s.runCalled.Load() {
		return ErrAlreadyRun
	}
	if h == nil {
		return fmt.Errorf("%w for %s %s", ErrMountNilHandler, method, path)
	}
	if !strings.HasPrefix(path, "/") {
		return fmt.Errorf("%w: path must begin with %q (got %q)", ErrMountBadPath, "/", path)
	}
	if strings.HasPrefix(path, "/h/") {
		return fmt.Errorf("%w: path must not repeat the /h/<prefix> prefix (got %q)", ErrMountBadPath, path)
	}
	method = strings.ToUpper(strings.TrimSpace(method))
	if !isAllowedMethod(method) {
		return fmt.Errorf("%w %q", ErrMountUnsupported, method)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mountedRoutes = append(s.mountedRoutes, mountedRoute{method: method, path: path, handler: h})
	return nil
}

// applyMounts registers each captured (method, path, handler) tuple on the
// supplied mux under the chassis prefix /h/<prefix>/...
func (s *Server) applyMounts(mux *http.ServeMux) {
	prefix := "/h/" + s.cfg.Server.PathPrefix
	s.mu.Lock()
	routes := append([]mountedRoute(nil), s.mountedRoutes...)
	s.mu.Unlock()
	for _, r := range routes {
		pattern := r.method + " " + prefix + r.path
		mux.Handle(pattern, r.handler)
	}
}

// isAllowedMethod reports whether m is one of the HTTP methods the chassis
// permits in [Server.Mount]. The chassis keeps the surface small to keep the
// mux behaviour predictable; the handlers only need POST and GET in v0.1.0.
func isAllowedMethod(m string) bool {
	switch m {
	case http.MethodPost, http.MethodGet, http.MethodPut, http.MethodDelete,
		http.MethodPatch, http.MethodHead, http.MethodOptions:
		return true
	default:
		return false
	}
}
