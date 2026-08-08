// Package cliflags carries the two global flags (--json, -v/--verbose) from
// root's PersistentPreRunE down to every verb via context, per the chassis
// Brief: "Both bind once at root, read via context by every verb. A verb
// never redeclares either flag."
package cliflags

import "context"

type contextKey struct{}

// Flags is the set of global flag values threaded through context.
type Flags struct {
	JSON    bool
	Verbose bool

	// AsRole is the role name this invocation acts as (`--as-role`, or the
	// WIP_AS_ROLE environment the agent harness sets once per role session);
	// empty means the human. Verbs resolve it through store.ActorFor, and the
	// write path verifies the claim against an open spawned role.
	AsRole string
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
