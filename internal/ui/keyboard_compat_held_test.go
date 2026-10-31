package ui

import (
	"io"
	"os"
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
