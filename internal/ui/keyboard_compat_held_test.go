package ui

import (
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/termreply"
)

// pastHoldWindow is comfortably longer than the dashboard's hold window for
// an unterminated escape string.
const pastHoldWindow = 150 * time.Millisecond

// With Option-as-Meta, Alt+Shift+- (the em-dash chord) sends ESC _, the APC
// introducer. On the dashboard the reply filter took it for the start of a
// terminal reply and swallowed every later key until a BEL or ST that typing
// never produces (#2974). Once the hold window has passed, the chord and the
// keys behind it come out as input.
func TestCSIuReaderReleasesTypedEscapeStringIntroducerAfterHoldWindow(t *testing.T) {
	t.Cleanup(termreply.Clear)
	termreply.Clear()
	r := newCSIuReader(nil)

	if got := r.consume([]byte("\x1b_"), false); len(got) != 0 {
		t.Fatalf("chord released before the hold window: %q", got)
	}
	time.Sleep(pastHoldWindow)
	if got := r.consume([]byte("ab"), false); string(got) != "\x1b_ab" {
		t.Fatalf("keys after the chord = %q, want %q", got, "\x1b_ab")
	}
	if got := r.consume([]byte("c"), false); string(got) != "c" {
		t.Fatalf("next key = %q, want %q", got, "c")
	}
}

// When no key follows, an idle check (consume with no bytes) releases the
// held chord once the window has passed.
func TestCSIuReaderIdleReleasesHeldChord(t *testing.T) {
	t.Cleanup(termreply.Clear)
	termreply.Clear()
	r := newCSIuReader(nil)

	if got := r.consume([]byte("\x1b_"), false); len(got) != 0 {
		t.Fatalf("chord released before the hold window: %q", got)
	}
	time.Sleep(pastHoldWindow)
	if got := r.consume(nil, false); string(got) != "\x1b_" {
		t.Fatalf("idle release = %q, want %q", got, "\x1b_")
	}
}

// A terminal reply ends with its terminator, also when split across reads,
// and is still dropped.
func TestCSIuReaderStillDropsTerminatedReplyWithHold(t *testing.T) {
	t.Cleanup(termreply.Clear)
	termreply.Clear()
	r := newCSIuReader(nil)

	if got := r.consume([]byte("\x1bP>|iTerm2 "), false); len(got) != 0 {
		t.Fatalf("reply head leaked: %q", got)
	}
	if got := r.consume([]byte("3.6.10\x1b\\k"), false); string(got) != "k" {
		t.Fatalf("after reply = %q, want %q", got, "k")
	}
	if got := r.consume([]byte("\x1b]0;title\x07z"), false); string(got) != "z" {
		t.Fatalf("after OSC reply = %q, want %q", got, "z")
	}
	time.Sleep(pastHoldWindow)
	if got := r.consume(nil, false); len(got) != 0 {
		t.Fatalf("idle check released reply bytes: %q", got)
	}
}

// An escape string begun while termreply.State is armed may be a reply, so
// it is dropped, not typed, once the hold window passes; the keys after it
// are input again.
func TestCSIuReaderDropsUnterminatedStringBegunWhenArmed(t *testing.T) {
	t.Cleanup(termreply.Clear)
	termreply.QuarantineFor(time.Second)
	r := newCSIuReader(nil)

	if got := r.consume([]byte("\x1b_a"), false); len(got) != 0 {
		t.Fatalf("armed escape string leaked: %q", got)
	}
	termreply.Clear()
	time.Sleep(pastHoldWindow)
	if got := r.consume([]byte("b"), false); string(got) != "b" {
		t.Fatalf("key after armed string = %q, want %q", got, "b")
	}
}

// readUntil reads r until it has produced want or the deadline passes.
func readUntil(t *testing.T, r io.Reader, want string) string {
	t.Helper()
	var got []byte
	buf := make([]byte, 64)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && len(got) < len(want) {
		n, err := r.Read(buf)
		got = append(got, buf[:n]...)
		if err != nil {
			break
		}
	}
	return string(got)
}

// The dashboard's input (NewTUIInput) releases the chord with no further
// key typed: nothing waits for a terminator.
func TestTUIInputReleasesTypedEscapeStringIntroducer(t *testing.T) {
	t.Cleanup(termreply.Clear)
	termreply.Clear()
	readFile, writeFile, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer readFile.Close()
	defer writeFile.Close()
	in := NewTUIInput(readFile)

	if _, err := io.WriteString(writeFile, "\x1b_"); err != nil {
		t.Fatal(err)
	}
	if got := readUntil(t, in, "\x1b_"); got != "\x1b_" {
		t.Fatalf("chord = %q, want %q", got, "\x1b_")
	}
	if _, err := io.WriteString(writeFile, "j"); err != nil {
		t.Fatal(err)
	}
	if got := readUntil(t, in, "j"); got != "j" {
		t.Fatalf("key after chord = %q, want %q", got, "j")
	}
}

// The embedded-terminal router's dashboard path does the same.
func TestSessionInputRouterDashboardReleasesTypedEscapeStringIntroducer(t *testing.T) {
	t.Cleanup(termreply.Clear)
	termreply.Clear()
	readFile, writeFile, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer readFile.Close()
	defer writeFile.Close()
	router := NewSessionInputRouter(readFile)
	defer router.Deactivate()

	if _, err := io.WriteString(writeFile, "\x1b_j"); err != nil {
		t.Fatal(err)
	}
	if got := readUntil(t, router, "\x1b_j"); got != "\x1b_j" {
		t.Fatalf("chord and key = %q, want %q", got, "\x1b_j")
	}
}

// Two terminal replies back to back, with the reads split after the first
// reply's ST and the second reply's introducer: the second hold gets its own
// full window, not what was left of the first one's, so its payload arriving
// a little later is still dropped as a reply.
func TestCSIuReaderSecondHoldGetsItsOwnWindow(t *testing.T) {
	t.Cleanup(termreply.Clear)
	termreply.Clear()
	r := newCSIuReader(nil)

	if got := r.consume([]byte("\x1b_first"), false); len(got) != 0 {
		t.Fatalf("first reply head leaked: %q", got)
	}
	time.Sleep(70 * time.Millisecond)
	if got := r.consume([]byte("\x1b\\\x1b_"), false); len(got) != 0 {
		t.Fatalf("first reply end or second introducer leaked: %q", got)
	}
	// Past the first hold's window, well inside the second's.
	time.Sleep(50 * time.Millisecond)
	if got := r.consume([]byte("second\x1b\\k"), false); string(got) != "k" {
		t.Fatalf("after second reply = %q, want %q", got, "k")
	}
}

// Unarmed, a held escape string longer than the filter's 4096-byte bound
// cannot be a reply: it is released as input at once, without waiting for
// the hold window, as the attach pump does (#2932). Armed, it is dropped.
func TestCSIuReaderOversizedHeldStringReleasedWhenUnarmed(t *testing.T) {
	t.Cleanup(termreply.Clear)
	termreply.Clear()
	r := newCSIuReader(nil)

	head := "\x1b_" + strings.Repeat("a", 4094) // 4096 bytes: still held
	if got := r.consume([]byte(head), false); len(got) != 0 {
		t.Fatalf("held string of 4096 bytes released: %d bytes", len(got))
	}
	if got := r.consume([]byte("a"), false); string(got) != head+"a" {
		t.Fatalf("4097th byte released %d bytes, want the whole string (%d)", len(got), len(head)+1)
	}
	if got := r.consume([]byte("q"), false); string(got) != "q" {
		t.Fatalf("key after release = %q, want %q", got, "q")
	}

	termreply.QuarantineFor(time.Second)
	armed := newCSIuReader(nil)
	if got := armed.consume([]byte(head+"a"), false); len(got) != 0 {
		t.Fatalf("armed oversized string leaked %d bytes", len(got))
	}
}

// When an escape string begun while armed is dropped at the end of its
// window, the dashboard input has nothing to return and must not then block
// in stdin.Read (#2692): tuiInput only called in because the hold was due.
func TestTUIInputDoesNotBlockAfterDroppingArmedHold(t *testing.T) {
	t.Cleanup(termreply.Clear)
	termreply.QuarantineFor(time.Second)
	readFile, writeFile, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer readFile.Close()
	defer writeFile.Close()
	in := NewTUIInput(readFile)

	if _, err := io.WriteString(writeFile, "\x1b_"); err != nil {
		t.Fatal(err)
	}
	done := make(chan []byte, 1)
	go func() {
		var got []byte
		buf := make([]byte, 64)
		for end := time.Now().Add(3 * pastHoldWindow); time.Now().Before(end); {
			n, _ := in.Read(buf)
			got = append(got, buf[:n]...)
		}
		done <- got
	}()
	select {
	case got := <-done:
		if len(got) != 0 {
			t.Fatalf("armed escape string leaked: %q", got)
		}
	case <-time.After(3*pastHoldWindow + 500*time.Millisecond):
		_, _ = io.WriteString(writeFile, "x") // unblock the reader
		<-done
		t.Fatal("Read blocked in stdin.Read after dropping the armed hold")
	}
}
