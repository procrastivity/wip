package readsurface

import (
	"context"
	"sort"

	"github.com/procrastivity/wip/internal/store"
)

// WaitingMatter groups the actionable reasons work is waiting under its root
// Matter. Gates retain their canonical requirement metadata. Dependencies
// retain the exact blocker and every exact node it holds, even when both ends
// belong to the same Matter.
type WaitingMatter struct {
	Matter       store.Node
	Gates        []store.GateRequirement
	Dependencies []WaitingDependency
}

// WaitingDependency is one unmet blocker and the work it currently holds.
type WaitingDependency struct {
	Blocker store.Node
	Held    []store.Node
}

// Waiting derives a Repo-scoped, Matter-grouped projection of work that is
// stalled now. A ready node or an in-progress leaf makes its Matter workable
// and suppresses future plan sequencing. For a stalled Matter, only the
// shallowest blocked nodes and open gates whose subject is Done remain.
func Waiting(ctx context.Context, v store.View, repo string, inProgress, ready []store.Node, finished []Finished, blocked []Blocked) ([]WaitingMatter, error) {
	type groupState struct {
		waiting  WaitingMatter
		gateKeys map[string]bool
		blockers map[string]int
		held     map[string]map[string]bool
	}

	workable := map[string]bool{}
	for _, node := range ready {
		if node.Repo == repo {
			workable[node.Matter] = true
		}
	}
	for _, node := range inProgress {
		if node.Repo != repo {
			continue
		}
		leaf := node.Kind == store.ScaleStep
		if !leaf {
			children, err := v.Children(ctx, node.ID)
			if err != nil {
				return nil, err
			}
			leaf = len(children) == 0
		}
		if leaf {
			workable[node.Matter] = true
		}
	}

	blockedIDs := map[string]bool{}
	for _, blockedNode := range blocked {
		if blockedNode.Node.Repo == repo && !workable[blockedNode.Node.Matter] {
			blockedIDs[blockedNode.Node.ID] = true
		}
	}
	var actionableBlocked []Blocked
	for _, blockedNode := range blocked {
		if blockedNode.Node.Repo != repo || workable[blockedNode.Node.Matter] {
			continue
		}
		drop := false
		for parentID := blockedNode.Node.Parent; parentID != ""; {
			if blockedIDs[parentID] {
				drop = true
				break
			}
			parent, err := v.Node(ctx, parentID)
			if err != nil {
				return nil, err
			}
			parentID = parent.Parent
		}
		if !drop {
			actionableBlocked = append(actionableBlocked, blockedNode)
		}
	}

	groups := map[string]*groupState{}
	group := func(matterID string) (*groupState, error) {
		if existing := groups[matterID]; existing != nil {
			return existing, nil
		}
		matter, err := v.Node(ctx, matterID)
		if err != nil {
			return nil, err
		}
		state := &groupState{
			waiting:  WaitingMatter{Matter: matter},
			gateKeys: map[string]bool{},
			blockers: map[string]int{},
			held:     map[string]map[string]bool{},
		}
		groups[matterID] = state
		return state, nil
	}

	for _, finishedNode := range finished {
		if finishedNode.Node.Repo != repo || workable[finishedNode.Node.Matter] {
			continue
		}
		for _, requirement := range finishedNode.Pending {
			if requirement.State != store.GateRequirementOpen || requirement.Subject.Lifecycle != store.Done {
				continue
			}
			state, err := group(finishedNode.Node.Matter)
			if err != nil {
				return nil, err
			}
			key := requirement.Gate + "\x00" + requirement.Subject.ID
			if state.gateKeys[key] {
				continue
			}
			state.gateKeys[key] = true
			state.waiting.Gates = append(state.waiting.Gates, requirement)
		}
	}

	for _, blockedNode := range actionableBlocked {
		state, err := group(blockedNode.Node.Matter)
		if err != nil {
			return nil, err
		}
		for _, blocker := range blockedNode.Blockers {
			index, exists := state.blockers[blocker.ID]
			if !exists {
				index = len(state.waiting.Dependencies)
				state.blockers[blocker.ID] = index
				state.held[blocker.ID] = map[string]bool{}
				state.waiting.Dependencies = append(state.waiting.Dependencies, WaitingDependency{Blocker: blocker})
			}
			if state.held[blocker.ID][blockedNode.Node.ID] {
				continue
			}
			state.held[blocker.ID][blockedNode.Node.ID] = true
			dependency := &state.waiting.Dependencies[index]
			dependency.Held = append(dependency.Held, blockedNode.Node)
		}
	}

	out := make([]WaitingMatter, 0, len(groups))
	for _, state := range groups {
		out = append(out, state.waiting)
	}
	// Node identities are monotonic ULIDs, so lexical order is creation order.
	sort.SliceStable(out, func(i, j int) bool { return out[i].Matter.ID < out[j].Matter.ID })
	return out, nil
}
