// Package cliflags carries root-level CLI flags from PersistentPreRunE down to
// verbs via context. This includes the presentation flags and the explicit
// experimental wipd profile opt-in.
package cliflags

import "context"

type contextKey struct{}

// Flags is the set of global flag values threaded through context.
type Flags struct {
	JSON                    bool
	Verbose                 bool
	ExperimentalWipdProfile string

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
