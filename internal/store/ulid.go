package store

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"sync"
	"time"
)

// IDLen is the character length of every identity in the store. Nobody types
// or reads one, so length is free and the collision domain is global (D44).
const IDLen = 26

// crockford is Crockford's base32 alphabet, as ULID specifies it.
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// idSource generates monotonic ULIDs: 48 bits of millisecond timestamp plus 80
// bits of randomness, rendered as 26 Crockford-base32 characters. Successive
// ids from one source sort strictly ascending as strings, even within the same
// millisecond (the random half is incremented rather than redrawn) — which is
// what lets an event's own identity double as the total-order key with no
// separate sequence column (D44, D51).
type idSource struct {
	mu       sync.Mutex
	lastMS   uint64
	lastRand [10]byte
	now      func() time.Time
}

func newIDSource() *idSource { return &idSource{now: time.Now} }

// next returns the next monotonic identity.
func (s *idSource) next() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	ms := uint64(s.now().UTC().UnixMilli())
	switch {
	case ms > s.lastMS:
		s.lastMS = ms
		if _, err := rand.Read(s.lastRand[:]); err != nil {
			// The store cannot mint identities without entropy, and an
			// identity that is not unique corrupts the log's total order.
			// There is no degraded mode worth having here.
			panic(fmt.Sprintf("store: entropy unavailable: %v", err))
		}
	default:
		// Same millisecond (or a clock that went backwards): increment the
		// random half so ordering stays strict. The consequence is that the
		// timestamp an id carries is non-decreasing even when the wall clock
		// is not — which is what makes occurred_at (derived from it, see
		// timeOfID) safe to sort on.
		ms = s.lastMS
		for i := len(s.lastRand) - 1; i >= 0; i-- {
			s.lastRand[i]++
			if s.lastRand[i] != 0 {
				break
			}
		}
	}

	var raw [16]byte
	var tsBuf [8]byte
	binary.BigEndian.PutUint64(tsBuf[:], ms)
	copy(raw[0:6], tsBuf[2:8])
	copy(raw[6:16], s.lastRand[:])

	return encodeCrockford(raw)
}

// encodeCrockford renders 16 bytes as the 26-character ULID text form.
func encodeCrockford(raw [16]byte) string {
	out := make([]byte, IDLen)
	// The first character carries only the top 3 bits of the 128-bit value,
	// which is why the first ten characters hold exactly the 48-bit timestamp.
	out[0] = crockford[(raw[0]&0xE0)>>5]
	var bitPos uint = 3
	for i := 1; i < IDLen; i++ {
		var v byte
		for j := 0; j < 5; j++ {
			byteIdx := bitPos >> 3
			bitIdx := 7 - (bitPos & 7)
			bit := (raw[byteIdx] >> bitIdx) & 1
			v = v<<1 | bit
			bitPos++
		}
		out[i] = crockford[v]
	}
	return string(out)
}

// timeOfID recovers the millisecond timestamp an identity carries.
//
// This is how the store answers `store-fork`'s "two clocks" finding without a
// twelfth envelope column: an event's occurred_at is *derived* from its own id
// rather than read from a second clock, so the two can never disagree and
// there is no `recorded_at` to reconcile. A foreign timestamp — an imported
// artifact's own date — belongs in a payload, never in occurred_at.
func timeOfID(id string) (time.Time, error) {
	if len(id) != IDLen {
		return time.Time{}, fmt.Errorf("store: %q is not a 26-character identity", id)
	}
	var ms uint64
	for i := 0; i < 10; i++ {
		v := crockfordValue(id[i])
		if v < 0 {
			return time.Time{}, fmt.Errorf("store: identity %q holds %q, which is not a Crockford base32 digit", id, id[i])
		}
		ms = ms<<5 | uint64(v)
	}
	return time.UnixMilli(int64(ms)).UTC(), nil
}

// IsIdentityShaped reports whether s has the fixed 26-character
// Crockford-Base32 shape every identity in this store uses — the shape test
// tier addressing dispatches on (tiers Brief, "Labels and addressing"): a
// locator of this shape is looked up as a ULID, anything else as a label.
func IsIdentityShaped(s string) bool {
	if len(s) != IDLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		if crockfordValue(s[i]) < 0 {
			return false
		}
	}
	return true
}

func crockfordValue(c byte) int {
	for i := 0; i < len(crockford); i++ {
		if crockford[i] == c {
			return i
		}
	}
	return -1
}
