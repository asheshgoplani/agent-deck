package recall

import (
	"sync"

	"github.com/klauspost/compress/zstd"
)

// Message bodies are stored zstd-compressed (3 to 4x on chat text). One
// encoder and one decoder, both single-threaded and low-memory, are shared
// process-wide: the backfill holds one body in flight at a time and the
// query side decompresses a few thousand small bodies per phrase search.
var (
	codecOnce sync.Once
	encoder   *zstd.Encoder
	decoder   *zstd.Decoder
)

func codec() (*zstd.Encoder, *zstd.Decoder) {
	codecOnce.Do(func() {
		encoder, _ = zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1), zstd.WithEncoderLevel(zstd.SpeedDefault),
			zstd.WithWindowSize(128<<10), zstd.WithLowerEncoderMem(true), zstd.WithZeroFrames(true))
		decoder, _ = zstd.NewReader(nil, zstd.WithDecoderConcurrency(1), zstd.WithDecoderLowmem(true))
	})
	return encoder, decoder
}

// CompressBody returns the zstd frame for text.
func CompressBody(text []byte) []byte {
	enc, _ := codec()
	return enc.EncodeAll(text, make([]byte, 0, len(text)/3+16))
}

// DecompressBody inverts CompressBody. A nil body (text_tier=none) yields nil.
func DecompressBody(body []byte) ([]byte, error) {
	if len(body) == 0 {
		return nil, nil
	}
	_, dec := codec()
	return dec.DecodeAll(body, nil)
}

// ClipBytes truncates text to at most n bytes on a UTF-8 boundary.
func ClipBytes(text string, n int) string {
	if len(text) <= n {
		return text
	}
	cut := n
	for cut > 0 && cut < len(text) && text[cut]&0xC0 == 0x80 {
		cut--
	}
	return text[:cut]
}
