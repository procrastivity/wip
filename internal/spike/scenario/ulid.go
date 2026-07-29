package scenario

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"sync"
	"time"
)

// crockford is Crockford's base32 alphabet, as ULID specifies it.
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// ULIDSource generates monotonic ULIDs: 48 bits of millisecond timestamp plus
// 80 bits of randomness, rendered as 26 Crockford-base32 characters.
// Successive ULIDs from one source sort strictly ascending as strings, even
// within the same millisecond (the random half is incremented rather than
// redrawn) — which is what lets an event's own identity double as the
// total-order key with no separate sequence column (D44, D51).
//
// Both spikes share this source so neither can win or lose the comparison on
// its identity generator. It is spike-grade: one process, one source.
type ULIDSource struct {
	mu       sync.Mutex
	lastMS   uint64
	lastRand [10]byte
	now      func() time.Time
}

// NewULIDSource returns a source using the real clock.
func NewULIDSource() *ULIDSource { return &ULIDSource{now: time.Now} }

// New returns the next monotonic ULID.
func (s *ULIDSource) New() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	ms := uint64(s.now().UTC().UnixMilli())
	switch {
	case ms > s.lastMS:
		s.lastMS = ms
		if _, err := rand.Read(s.lastRand[:]); err != nil {
			panic(fmt.Sprintf("spike: entropy unavailable: %v", err))
		}
	default:
		// Same millisecond (or a clock that went backwards): increment the
		// random half so ordering stays strict.
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
	out := make([]byte, 26)
	// The first character carries only the top 3 bits of the 128-bit value.
	out[0] = crockford[(raw[0]&0xE0)>>5]
	var bitPos uint = 3
	for i := 1; i < 26; i++ {
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
