// Package cliflags carries the two global flags (--json, -v/--verbose) from
// root's PersistentPreRunE down to every verb via context, per the chassis
// Brief: "Both bind once at root, read via context by every verb. A verb
// never redeclares either flag."
package cliflags

import "context"

type contextKey struct{}

// Flags is the pair of global flag values threaded through context.
type Flags struct {
	JSON    bool
	Verbose bool
}

// WithFlags returns a context carrying f, for verbs to read via FromContext.
func WithFlags(ctx context.Context, f Flags) context.Context {
	return context.WithValue(ctx, contextKey{}, f)
}

// FromContext returns the Flags stored by WithFlags, or the zero value if
// none were stored.
func FromContext(ctx context.Context) Flags {
	f, _ := ctx.Value(contextKey{}).(Flags)
	return f
}
