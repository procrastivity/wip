package wipd

import (
	"context"
	"sync"
)

// executionLanes serializes handler dispatches by the domain key carried in an
// M2 command. In the local fixture path it is synthetic scheduling data, not
// an authenticated authority identity. Entries are removed after their last
// holder or waiter leaves; the exchange limit bounds the live-key set.
type executionLanes struct {
	mu    sync.Mutex
	byKey map[string]*executionLane
}

type executionLane struct {
	permit     chan struct{}
	references int
	waiters    int
}

func (lanes *executionLanes) acquire(ctx context.Context, key string) (func(), bool) {
	lanes.mu.Lock()
	if lanes.byKey == nil {
		lanes.byKey = make(map[string]*executionLane)
	}
	lane := lanes.byKey[key]
	if lane == nil {
		lane = &executionLane{permit: make(chan struct{}, 1)}
		lanes.byKey[key] = lane
	}
	lane.references++
	lane.waiters++
	lanes.mu.Unlock()

	select {
	case lane.permit <- struct{}{}:
		lanes.mu.Lock()
		lane.waiters--
		lanes.mu.Unlock()

		var once sync.Once
		return func() {
			once.Do(func() {
				<-lane.permit
				lanes.releaseReference(key, lane)
			})
		}, true
	case <-ctx.Done():
		lanes.mu.Lock()
		lane.waiters--
		lane.references--
		if lane.references == 0 && lanes.byKey[key] == lane {
			delete(lanes.byKey, key)
		}
		lanes.mu.Unlock()
		return nil, false
	}
}

func (lanes *executionLanes) releaseReference(key string, lane *executionLane) {
	lanes.mu.Lock()
	defer lanes.mu.Unlock()
	lane.references--
	if lane.references == 0 && lanes.byKey[key] == lane {
		delete(lanes.byKey, key)
	}
}

func (lanes *executionLanes) occupancy(key string) (active, waiting int) {
	lanes.mu.Lock()
	defer lanes.mu.Unlock()
	if lane := lanes.byKey[key]; lane != nil {
		return len(lane.permit), lane.waiters
	}
	return 0, 0
}

// dispatchGate gives cancellation and the handler-dispatch boundary one
// linearization point. A cancel that wins while work is queued guarantees the
// Registry is never dispatched; after begin, cancellation stops only waiting.
type dispatchGate struct {
	mu         sync.Mutex
	dispatched bool
	cancelled  bool
	cancel     context.CancelFunc
}

func (gate *dispatchGate) begin() bool {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if gate.cancelled {
		return false
	}
	gate.dispatched = true
	return true
}

func (gate *dispatchGate) cancelBeforeDispatch() bool {
	gate.mu.Lock()
	if gate.dispatched || gate.cancelled {
		gate.mu.Unlock()
		return false
	}
	gate.cancelled = true
	gate.mu.Unlock()
	gate.cancel()
	return true
}
