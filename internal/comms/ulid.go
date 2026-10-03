package comms

import (
	"crypto/rand"
	"encoding/binary"
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

	var raw [16]byte
	raw[0] = byte(ms >> 40)
	raw[1] = byte(ms >> 32)
	raw[2] = byte(ms >> 24)
	raw[3] = byte(ms >> 16)
	raw[4] = byte(ms >> 8)
	raw[5] = byte(ms)
	copy(raw[6:], entropy[:])
	return encodeBase32(raw)
}

// encodeBase32 renders 128 bits as 26 Crockford base32 characters (the top
// two bits of the first character are zero, as the spec requires).
func encodeBase32(b [16]byte) string {
	var out [26]byte
	out[0] = crockford[(b[0]&224)>>5]
	out[1] = crockford[b[0]&31]
	out[2] = crockford[(b[1]&248)>>3]
	out[3] = crockford[((b[1]&7)<<2)|((b[2]&192)>>6)]
	out[4] = crockford[(b[2]&62)>>1]
	out[5] = crockford[((b[2]&1)<<4)|((b[3]&240)>>4)]
	out[6] = crockford[((b[3]&15)<<1)|((b[4]&128)>>7)]
	out[7] = crockford[(b[4]&124)>>2]
	out[8] = crockford[((b[4]&3)<<3)|((b[5]&224)>>5)]
	out[9] = crockford[b[5]&31]
	out[10] = crockford[(b[6]&248)>>3]
	out[11] = crockford[((b[6]&7)<<2)|((b[7]&192)>>6)]
	out[12] = crockford[(b[7]&62)>>1]
	out[13] = crockford[((b[7]&1)<<4)|((b[8]&240)>>4)]
	out[14] = crockford[((b[8]&15)<<1)|((b[9]&128)>>7)]
	out[15] = crockford[(b[9]&124)>>2]
	out[16] = crockford[((b[9]&3)<<3)|((b[10]&224)>>5)]
	out[17] = crockford[b[10]&31]
	out[18] = crockford[(b[11]&248)>>3]
	out[19] = crockford[((b[11]&7)<<2)|((b[12]&192)>>6)]
	out[20] = crockford[(b[12]&62)>>1]
	out[21] = crockford[((b[12]&1)<<4)|((b[13]&240)>>4)]
	out[22] = crockford[((b[13]&15)<<1)|((b[14]&128)>>7)]
	out[23] = crockford[(b[14]&124)>>2]
	out[24] = crockford[((b[14]&3)<<3)|((b[15]&224)>>5)]
	out[25] = crockford[b[15]&31]
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

func decodeChar(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'A' && c <= 'Z':
		for i := 10; i < len(crockford); i++ {
			if crockford[i] == c {
				return i
			}
		}
	case c >= 'a' && c <= 'z':
		return decodeChar(c - 32)
	}
	return -1
}
