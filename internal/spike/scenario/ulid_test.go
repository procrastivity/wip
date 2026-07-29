package scenario

import "testing"

// The identity generator is shared by both spikes, so its two load-bearing
// properties — 26-character text form, strictly ascending within a
// millisecond — are checked here rather than twice downstream.
func TestULIDSourceIsMonotonicAndWellFormed(t *testing.T) {
	src := NewULIDSource()
	prev := ""
	for i := 0; i < 10_000; i++ {
		id := src.New()
		if len(id) != 26 {
			t.Fatalf("ulid %q has length %d, want 26", id, len(id))
		}
		for _, c := range id {
			if !isCrockford(byte(c)) {
				t.Fatalf("ulid %q contains %q, not a Crockford base32 character", id, c)
			}
		}
		if id <= prev {
			t.Fatalf("ulid %d: %q does not sort after %q", i, id, prev)
		}
		prev = id
	}
}

func isCrockford(c byte) bool {
	for i := 0; i < len(crockford); i++ {
		if crockford[i] == c {
			return true
		}
	}
	return false
}
