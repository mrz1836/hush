package server

import "context"

// requestIDKeyType is the typed empty struct used as the context key for the
// chassis-assigned request ID. Using a typed struct (rather than a string)
// guarantees that no caller outside this package can accidentally collide
// with the key by passing the same string literal to context.WithValue.
type requestIDKeyType struct{}

// requestIDKey is the singleton context key value used to store and
// retrieve the request ID.
//
//nolint:gochecknoglobals // sentinel-class: typed empty-struct context key, immutable after init
var requestIDKey = requestIDKeyType{}

// RequestID returns the chassis-assigned request identifier carried by ctx.
// Returns the empty string when the context did not pass through the
// chassis middleware (e.g. a unit test that builds the context manually).
//
// In production, every request reaches the handler with a 32-character
// lowercase hex ID — the request-ID middleware is the first link in the
// middleware chain.
func RequestID(ctx context.Context) string {
	if v, ok := ctx.Value(requestIDKey).(string); ok {
		return v
	}
	return ""
}
