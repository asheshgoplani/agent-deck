package ui

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/termreply"
	tea "github.com/charmbracelet/bubbletea"
)

// Regression tests for #2826 (reported by cherninely, who also wrote the
// first version of these tests): csiuReader.Read cut its output at len(p)
// even when bytes held from an earlier source read made it longer, so an SGR
// mouse report reached Bubble Tea split across two Reads and its tail decoded
// as keys ("alt+[", "<", "6", ...). The '<' shrank the preview.

const splitMouseToken = "\x1b[<65;166;31M"

// overflowingMouseInput sends the first split bytes of one report, then the
// rest of it plus enough whole reports that the second chunk fills a size-byte
// buffer: the held bytes push the reader's output past size.
func overflowingMouseInput(size, split int) (io.Reader, int) {
	count := size/len(splitMouseToken) + 1
	return NewCSIuReader(io.MultiReader(
		strings.NewReader(splitMouseToken[:split]),
		strings.NewReader(splitMouseToken[split:]+strings.Repeat(splitMouseToken, count-1)),
	)), count
}

func TestCSIuReaderPreservesMouseBoundariesOnOverflow_2826(t *testing.T) {
	t.Cleanup(termreply.Clear)
	for _, armed := range []bool{false, true} {
		t.Run(fmt.Sprintf("armed=%t", armed), func(t *testing.T) {
			termreply.Clear()
			if armed {
				termreply.QuarantineFor(time.Minute)
			}
			for _, size := range []int{32, 64, 128, 256, 512} {
				for split := 1; split < len(splitMouseToken); split++ {
					t.Run(fmt.Sprintf("size=%d/split=%d", size, split), func(t *testing.T) {
						r, want := overflowingMouseInput(size, split)
						buf := make([]byte, size)
						got := 0
						for {
							n, err := r.Read(buf)
							if n > 0 {
								chunk := buf[:n]
								if !bytes.Equal(chunk, bytes.Repeat([]byte(splitMouseToken), n/len(splitMouseToken))) {
									t.Fatalf("Read split a mouse event: %q", chunk)
								}
								got += n / len(splitMouseToken)
							}
							if err == io.EOF {
								break
							}
							if err != nil {
								t.Fatal(err)
							}
						}
						if got != want {
							t.Fatalf("received %d mouse events, want %d", got, want)
						}
					})
				}
			}
		})
	}
}

// Buffers too small for a token still get every byte, in order, and an empty
// buffer returns at once.
func TestCSIuReaderSmallAndEmptyBuffersPreserveInput_2826(t *testing.T) {
	const input = splitMouseToken + "<>\x1b[AS\x1bxé\x1bOH\x1b"
	const want = splitMouseToken + "<>\x1b[AS\x1bxé\x1b[H\x1b"
	for _, size := range []int{1, 2, 3, 5, len(splitMouseToken)} {
		t.Run(fmt.Sprintf("size=%d", size), func(t *testing.T) {
			r := NewCSIuReader(strings.NewReader(input))
			type result struct {
				n   int
				err error
			}
			empty := make(chan result, 1)
			go func() {
				n, err := r.Read(nil)
				empty <- result{n, err}
			}()
			select {
			case got := <-empty:
				if got.n != 0 || got.err != nil {
					t.Fatalf("empty Read = %d, %v", got.n, got.err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("empty Read did not return") // the reader is lost to that goroutine
			}
			var got bytes.Buffer
			buf := make([]byte, size)
			for {
				n, err := r.Read(buf)
				got.Write(buf[:n])
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			if got.String() != want {
				t.Fatalf("input changed: got %q, want %q", got.String(), want)
			}
		})
	}
}

// Every Read ends on a token boundary for keys, alt+keys and escape sequences
// too, whatever the buffer size and wherever held bytes end. (A multi-byte
// rune or an ESC ESC the source itself splits is not held back; that is not
// this cut.)
func TestCSIuReaderReadsEndOnTokenBoundaries_2826(t *testing.T) {
	tokens := []string{splitMouseToken, "a", "\x1bx", "\x1b[A", "\x1bOP", "\x1b[200~", "<", "\x1b[<0;5;7m"}
	var stream string
	bounds := map[int]bool{0: true}
	for range 8 {
		for _, tok := range tokens {
			stream += tok
			bounds[len(stream)] = true
		}
	}
	for size := len(splitMouseToken); size <= 64; size++ {
		for held := 1; held < len(splitMouseToken); held++ {
			r := NewCSIuReader(io.MultiReader(strings.NewReader(stream[:held]), strings.NewReader(stream[held:])))
			buf := make([]byte, size)
			var got bytes.Buffer
			for {
				n, err := r.Read(buf)
				got.Write(buf[:n])
				if n > 0 && !bounds[got.Len()] {
					t.Fatalf("size=%d held=%d: Read ended inside a token: %q", size, held, buf[:n])
				}
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			if got.String() != stream {
				t.Fatalf("size=%d held=%d: input changed: got %q", size, held, got.String())
			}
		}
	}
}

type splitMouseModel struct {
	home  *Home
	want  int
	mouse int
	keys  []string
}

func (m *splitMouseModel) Init() tea.Cmd { return nil }
func (m *splitMouseModel) View() string  { return "" }
func (m *splitMouseModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	m.home.Update(msg)
	switch msg := msg.(type) {
	case tea.MouseMsg:
		m.mouse++
	case tea.KeyMsg:
		m.keys = append(m.keys, msg.String())
		if strings.Contains(msg.String(), "<") {
			return m, tea.Quit
		}
	}
	if m.mouse == m.want {
		return m, tea.Quit
	}
	return m, nil
}

// The reporter's scenario end to end: a wheel burst through the real reader,
// Bubble Tea's decoder and Home.Update leaves the 75% preview alone.
func TestCSIuMouseBurstDoesNotResizePreview_2826(t *testing.T) {
	r, count := overflowingMouseInput(256, len(splitMouseToken)-1)
	home := newTestHomeWithItems(171, 53, nil)
	home.previewPct = 75
	model := &splitMouseModel{home: home, want: count}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p := tea.NewProgram(model, tea.WithInput(r), tea.WithOutput(io.Discard),
		tea.WithoutRenderer(), tea.WithoutSignalHandler(), tea.WithContext(ctx))
	if _, err := p.Run(); err != nil {
		t.Fatal(err)
	}
	if model.mouse != count || len(model.keys) != 0 || home.getPreviewPct() != 75 {
		t.Fatalf("mouse=%d/%d keys=%q preview=%d%%; want only mouse events and preview=75%%",
			model.mouse, count, model.keys, home.getPreviewPct())
	}
}
