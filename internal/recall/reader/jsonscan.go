package reader

import (
	"bufio"
	"errors"
	"fmt"
	"io"
)

// jsonScanner walks one JSON document structurally with a bounded buffer:
// it never materialises a value longer than the limit its caller passes,
// so a 10 MB message inside a 31 MB Gemini document costs a bufio buffer
// and nothing else. encoding/json's Decoder would buffer the whole value
// (and copy it once more into a RawMessage), which is what blew the
// design's RSS budget.
type jsonScanner struct {
	br  *bufio.Reader
	off int64 // bytes consumed so far
}

var errJSONShape = errors.New("recall: unexpected JSON shape")

func newJSONScanner(r io.Reader) *jsonScanner {
	return &jsonScanner{br: bufio.NewReaderSize(r, 256<<10)}
}

func (s *jsonScanner) readByte() (byte, error) {
	b, err := s.br.ReadByte()
	if err == nil {
		s.off++
	}
	return b, err
}

// skipWS consumes whitespace and returns the next byte without consuming it.
func (s *jsonScanner) skipWS() (byte, error) {
	for {
		b, err := s.br.ReadByte()
		if err != nil {
			return 0, err
		}
		switch b {
		case ' ', '\t', '\r', '\n':
			s.off++
			continue
		}
		return b, s.br.UnreadByte()
	}
}

// expect consumes one byte after whitespace and checks it.
func (s *jsonScanner) expect(want byte) error {
	b, err := s.skipWS()
	if err != nil {
		return err
	}
	if b != want {
		return fmt.Errorf("%w: want %q at byte %d, got %q", errJSONShape, want, s.off, b)
	}
	_, err = s.readByte()
	return err
}

// value consumes one JSON value and returns its bytes when it is at most
// limit bytes long; longer values are consumed but not kept (tooLong).
// start is the value's byte offset in the document and n its length.
func (s *jsonScanner) value(limit int) (raw []byte, start, n int64, tooLong bool, err error) {
	if _, err = s.skipWS(); err != nil {
		return nil, 0, 0, false, err
	}
	start = s.off
	buf := make([]byte, 0, 512)
	keep := func(b byte) {
		if tooLong {
			return
		}
		if len(buf) >= limit {
			tooLong = true
			buf = nil
			return
		}
		buf = append(buf, b)
	}
	depth := 0
	inStr, esc := false, false
	for {
		b, err := s.readByte()
		if err != nil {
			return nil, start, s.off - start, tooLong, err
		}
		keep(b)
		if inStr {
			switch {
			case esc:
				esc = false
			case b == '\\':
				esc = true
			case b == '"':
				inStr = false
				if depth == 0 {
					return buf, start, s.off - start, tooLong, nil
				}
			}
			continue
		}
		switch b {
		case '"':
			inStr = true
		case '{', '[':
			depth++
		case '}', ']':
			depth--
			if depth == 0 {
				return buf, start, s.off - start, tooLong, nil
			}
			if depth < 0 {
				return nil, start, s.off - start, tooLong, errJSONShape
			}
		case ',':
			if depth == 0 {
				// A scalar ends at the comma, which belongs to the parent.
				s.off--
				buf = buf[:len(buf)-1]
				return buf, start, s.off - start, tooLong, s.br.UnreadByte()
			}
		default:
			if depth == 0 {
				// Scalars (numbers, true/false/null) end at whitespace or a
				// closing bracket of the parent.
				if next, err := s.br.Peek(1); err == nil && (next[0] == ',' || next[0] == '}' || next[0] == ']' || next[0] == ' ' || next[0] == '\n' || next[0] == '\r' || next[0] == '\t') {
					return buf, start, s.off - start, tooLong, nil
				}
			}
		}
	}
}

// delim consumes the next non-space byte, which must be one of the given
// delimiters, and returns it.
func (s *jsonScanner) delim(allowed ...byte) (byte, error) {
	b, err := s.skipWS()
	if err != nil {
		return 0, err
	}
	for _, a := range allowed {
		if b == a {
			_, err = s.readByte()
			return b, err
		}
	}
	return 0, fmt.Errorf("%w: got %q at byte %d", errJSONShape, b, s.off)
}
