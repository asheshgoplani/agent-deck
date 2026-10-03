package comms

import (
	"crypto/rand"
	"encoding/binary"
	"strings"
	"sync"
	"time"
)

// ULID (github.com/ulid/spec): 48 bits of Unix milliseconds then 80 random
// bits, Crockford base32, 26 characters, lexically sortable by time. Written
// here rather than imported: the ledger needs exactly this one function and
// the repo adds no dependency for it.

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

var (
	ulidMu       sync.Mutex
	ulidLastMS   int64
	ulidLastRand [10]byte
)

// NewID returns a new ULID for the given instant. Ids minted within the same
// millisecond by this process increment the random part so they stay
// strictly ordered; across processes ordering within a millisecond is random,
// which the ledger does not rely on (the bus cursor orders records).
func NewID(now time.Time) string {
	ms := now.UnixMilli()
	var entropy [10]byte
	ulidMu.Lock()
	if ms == ulidLastMS {
		entropy = ulidLastRand
		for i := len(entropy) - 1; i >= 0; i-- {
			entropy[i]++
			if entropy[i] != 0 {
				break
			}
		}
	} else {
		if _, err := rand.Read(entropy[:]); err != nil {
			binary.BigEndian.PutUint64(entropy[2:], uint64(now.UnixNano())) //nolint:gosec // G115: fallback entropy only
		}
	}
	ulidLastMS, ulidLastRand = ms, entropy
	ulidMu.Unlock()

	// The 48-bit time field is the low six bytes of the big-endian ms.
	var ts [8]byte
	binary.BigEndian.PutUint64(ts[:], uint64(ms)) //nolint:gosec // G115: Unix ms is positive for any real clock
	var raw [16]byte
	copy(raw[:6], ts[2:])
	copy(raw[6:], entropy[:])
	return encodeBase32(raw)
}

// encodeBase32 renders 128 bits as 26 Crockford base32 characters, five
// bits each, most significant first (the spec's two leading pad bits make
// 130).
func encodeBase32(b [16]byte) string {
	var out [26]byte
	var acc uint64
	bits := uint(2)
	n := 0
	for _, x := range b {
		acc = acc<<8 | uint64(x)
		bits += 8
		for bits >= 5 {
			bits -= 5
			out[n] = crockford[(acc>>bits)&31]
			n++
		}
	}
	return string(out[:])
}

// IDTime decodes the millisecond timestamp of a ULID (zero time when the id
// is malformed). Used by retention and stats so a record's age needs no
// second field.
func IDTime(id string) time.Time {
	if len(id) != 26 {
		return time.Time{}
	}
	var ms int64
	for i := 0; i < 10; i++ {
		v := decodeChar(id[i])
		if v < 0 {
			return time.Time{}
		}
		ms = ms<<5 | int64(v)
	}
	return time.UnixMilli(ms)
}

// decodeChar returns the value of one Crockford base32 character, case
// insensitive, or -1 for a character outside the alphabet.
func decodeChar(c byte) int {
	if c >= 'a' && c <= 'z' {
		c -= 'a' - 'A'
	}
	return strings.IndexByte(crockford, c)
}
