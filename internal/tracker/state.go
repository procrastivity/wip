package tracker

import "context"

// LiveClass is the provider-neutral lifecycle class of a tracker item.
type LiveClass string

// LiveClass values keep provider-specific workflow names outside callers.
const (
	LiveBacklog     LiveClass = "backlog"
	LiveActive      LiveClass = "active"
	LiveNonterminal LiveClass = "nonterminal"
	LiveCompleted   LiveClass = "completed"
	LiveCanceled    LiveClass = "canceled"
	LiveTerminal    LiveClass = "terminal"
)

// LiveState is one current provider observation. Display retains the
// provider's state vocabulary for operator output. Lease identifies the
// observed revision for a later guarded write.
type LiveState struct {
	Class   LiveClass
	Display string
	Lease   string
}

// StateReader reads the current provider state of one tracker reference.
type StateReader interface {
	ReadState(context.Context, string) (LiveState, error)
}
