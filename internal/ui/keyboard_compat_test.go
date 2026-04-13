package ui

import (
	"bytes"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/termreply"
)

type chunkedReader struct {
	chunks [][]byte
	index  int
}

func (r *chunkedReader) Read(p []byte) (int, error) {
	if r.index >= len(r.chunks) {
		return 0, io.EOF
	}
	n := copy(p, r.chunks[r.index])
	r.index++
	return n, nil
}

// TestParseCSIu tests the CSI u sequence parser.
func TestParseCSIu(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantNil   bool
		wantRunes []rune
		wantType  int // -1 = don't check type (expect KeyRunes)
	}{
		{
			name:      "Shift+m produces uppercase M",
			input:     "\x1b[109;2u",
			wantRunes: []rune{'M'},
		},
		{
			name:      "Shift+r produces uppercase R",
			input:     "\x1b[114;2u",
			wantRunes: []rune{'R'},
		},
		{
			name:      "Shift+f produces uppercase F",
			input:     "\x1b[102;2u",
			wantRunes: []rune{'F'},
		},
		{
			name:      "no modifier produces lowercase m",
			input:     "\x1b[109u",
			wantRunes: []rune{'m'},
		},
		{
			name:      "Ctrl+a modifier",
			input:     "\x1b[97;5u",
			wantRunes: []rune{1}, // ctrl+a = rune 1
		},
		{
			name:    "codepoint 13 returns KeyEnter",
			input:   "\x1b[13u",
			wantNil: false,
		},
		{
			name:    "not a CSI u sequence returns nil",
			input:   "not a csi u",
			wantNil: true,
		},
		{
			name:    "plain arrow sequence returns nil",
			input:   "\x1b[A",
			wantNil: true,
		},
		{
			name:    "empty string returns nil",
			input:   "",
			wantNil: true,
		},
		{
			name:      "space codepoint 32",
			input:     "\x1b[32u",
			wantRunes: nil, // KeySpace type expected, not runes
		},
		{
			name:      "Shift+underscore codepoint 95",
			input:     "\x1b[95;2u",
			wantRunes: []rune{'_'},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ParseCSIu([]byte(tt.input))

			if tt.wantNil {
				if result != nil {
					t.Fatalf("ParseCSIu(%q) = %+v, want nil", tt.input, result)
				}
				return
			}

			if result == nil {
				t.Fatalf("ParseCSIu(%q) = nil, want non-nil", tt.input)
			}

			if tt.wantRunes != nil && string(result.Runes) != string(tt.wantRunes) {
				t.Errorf("ParseCSIu(%q).Runes = %v, want %v", tt.input, result.Runes, tt.wantRunes)
			}
		})
	}
}

// TestParseCSIuCtrlA verifies Ctrl+a specifically (modifier=5 means shift+ctrl,
// but modifier=5 from Kitty means ctrl only (1+4=5)).
func TestParseCSIuCtrlA(t *testing.T) {
	// modifier 5 = 1 (no mod base) + 4 (ctrl) = ctrl only
	result := ParseCSIu([]byte("\x1b[97;5u"))
	if result == nil {
		t.Fatal("expected non-nil result for ctrl+a")
	}
	// ctrl+a should be rune 1 (ctrl sequence) or specific key type
	// Either runes=[1] or a ctrl key type is acceptable
	if len(result.Runes) > 0 && result.Runes[0] != 1 {
		t.Errorf("ctrl+a: expected rune 1, got %v", result.Runes[0])
	}
}

// TestDisableKittyKeyboard tests that DisableKittyKeyboard writes the correct escape sequence.
func TestDisableKittyKeyboard(t *testing.T) {
	var buf bytes.Buffer
	DisableKittyKeyboard(&buf)
	got := buf.String()
	want := "\x1b[<u"
	if got != want {
		t.Errorf("DisableKittyKeyboard wrote %q, want %q", got, want)
	}
}

// TestEnableKittyKeyboard tests that EnableKittyKeyboard pushes mode 1.
func TestEnableKittyKeyboard(t *testing.T) {
	var buf bytes.Buffer
	EnableKittyKeyboard(&buf)
	got := buf.String()
	want := "\x1b[>1u"
	if got != want {
		t.Errorf("EnableKittyKeyboard wrote %q, want %q", got, want)
	}
}

// TestKittyKeyboardPushPopBalance verifies that EnableKittyKeyboard (push mode 1)
// followed by DisableKittyKeyboard (pop) produces balanced escape sequences.
func TestKittyKeyboardPushPopBalance(t *testing.T) {
	var buf bytes.Buffer
	EnableKittyKeyboard(&buf)
	DisableKittyKeyboard(&buf)
	got := buf.String()
	want := "\x1b[>1u\x1b[<u"
	if got != want {
		t.Errorf("push+pop sequence = %q, want %q", got, want)
	}
}

// TestRestoreKittyKeyboard tests that RestoreKittyKeyboard writes the correct escape sequence.
func TestRestoreKittyKeyboard(t *testing.T) {
	var buf bytes.Buffer
	RestoreKittyKeyboard(&buf)
	got := buf.String()
	want := "\x1b[<u"
	if got != want {
		t.Errorf("RestoreKittyKeyboard wrote %q, want %q", got, want)
	}
}

// TestCSIuReaderPassesCSIuShiftM verifies CSIuReader translates \x1b[109;2u to "M".
func TestCSIuReaderPassesCSIuShiftM(t *testing.T) {
	input := "\x1b[109;2u"
	r := NewCSIuReader(bytes.NewReader([]byte(input)))
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll error: %v", err)
	}
	if string(out) != "M" {
		t.Errorf("CSIuReader translated %q to %q, want %q", input, string(out), "M")
	}
}

// TestCSIuReaderPassesNormalASCII verifies plain ASCII passes through unchanged.
func TestCSIuReaderPassesNormalASCII(t *testing.T) {
	input := "hello world"
	r := NewCSIuReader(bytes.NewReader([]byte(input)))
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll error: %v", err)
	}
	if string(out) != input {
		t.Errorf("CSIuReader changed plain ASCII: got %q, want %q", string(out), input)
	}
}

// TestCSIuReaderPassesStandardEscapeSequences verifies standard sequences pass through.
func TestCSIuReaderPassesStandardEscapeSequences(t *testing.T) {
	// \x1b[A is the up-arrow sequence — not a CSI u sequence
	input := "\x1b[A"
	r := NewCSIuReader(bytes.NewReader([]byte(input)))
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll error: %v", err)
	}
	if string(out) != input {
		t.Errorf("CSIuReader modified standard escape sequence: got %q, want %q", string(out), input)
	}
}

// TestCSIuReaderPassesSGRMouseEvents verifies that SGR mouse sequences
// (final byte 'M'/'m') pass through correctly and don't eat subsequent input.
// Regression test: the original terminator set omitted 'M' and 'm', causing
// mouse events to consume following arrow key sequences.
func TestCSIuReaderPassesSGRMouseEvents(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "mouse press then arrow up",
			input: "\x1b[<0;10;20M\x1b[A",
			want:  "\x1b[<0;10;20M\x1b[A",
		},
		{
			name:  "mouse release then arrow down",
			input: "\x1b[<0;10;20m\x1b[B",
			want:  "\x1b[<0;10;20m\x1b[B",
		},
		{
			name:  "mouse move then keystroke",
			input: "\x1b[<35;5;15M" + "j",
			want:  "\x1b[<35;5;15M" + "j",
		},
		{
			name:  "mouse between two arrows",
			input: "\x1b[A\x1b[<0;1;1M\x1b[B",
			want:  "\x1b[A\x1b[<0;1;1M\x1b[B",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewCSIuReader(bytes.NewReader([]byte(tt.input)))
			out, err := io.ReadAll(r)
			if err != nil {
				t.Fatalf("ReadAll error: %v", err)
			}
			if string(out) != tt.want {
				t.Errorf("got %q, want %q", string(out), tt.want)
			}
		})
	}
}

// TestCSIuReaderMixedInput verifies mixed input is correctly handled.
func TestCSIuReaderMixedInput(t *testing.T) {
	// "a" + shift+r CSI u + "b"
	input := "a\x1b[114;2ub"
	r := NewCSIuReader(bytes.NewReader([]byte(input)))
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll error: %v", err)
	}
	want := "aRb"
	if string(out) != want {
		t.Errorf("CSIuReader mixed: got %q, want %q", string(out), want)
	}
}

// TestParseModifyOtherKeys tests the xterm modifyOtherKeys parser.
func TestParseModifyOtherKeys(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantNil   bool
		wantRunes []rune
	}{
		{
			name:      "Shift+S (modifier=2, codepoint=83)",
			input:     "\x1b[27;2;83~",
			wantRunes: []rune{'S'},
		},
		{
			name:      "Shift+N (modifier=2, codepoint=78)",
			input:     "\x1b[27;2;78~",
			wantRunes: []rune{'N'},
		},
		{
			name:      "Shift+R (modifier=2, codepoint=114)",
			input:     "\x1b[27;2;114~",
			wantRunes: []rune{'R'},
		},
		{
			name:      "no shift lowercase s (modifier=1, codepoint=115)",
			input:     "\x1b[27;1;115~",
			wantRunes: []rune{'s'},
		},
		{
			name:      "Ctrl+a (modifier=5, codepoint=97)",
			input:     "\x1b[27;5;97~",
			wantRunes: []rune{1},
		},
		{
			name:    "not modifyOtherKeys (wrong prefix)",
			input:   "\x1b[28;2;83~",
			wantNil: true,
		},
		{
			name:    "empty returns nil",
			input:   "",
			wantNil: true,
		},
		{
			name:    "too short returns nil",
			input:   "\x1b[27;2~",
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ParseModifyOtherKeys([]byte(tt.input))

			if tt.wantNil {
				if result != nil {
					t.Fatalf("ParseModifyOtherKeys(%q) = %+v, want nil", tt.input, result)
				}
				return
			}

			if result == nil {
				t.Fatalf("ParseModifyOtherKeys(%q) = nil, want non-nil", tt.input)
			}

			if tt.wantRunes != nil && string(result.Runes) != string(tt.wantRunes) {
				t.Errorf("ParseModifyOtherKeys(%q).Runes = %v, want %v", tt.input, result.Runes, tt.wantRunes)
			}
		})
	}
}

// TestCSIuReaderModifyOtherKeys verifies the reader translates modifyOtherKeys sequences.
func TestCSIuReaderModifyOtherKeys(t *testing.T) {
	// Shift+S via modifyOtherKeys: ESC[27;2;83~
	input := "\x1b[27;2;83~"
	r := NewCSIuReader(bytes.NewReader([]byte(input)))
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll error: %v", err)
	}
	if string(out) != "S" {
		t.Errorf("CSIuReader modifyOtherKeys: got %q, want %q", string(out), "S")
	}
}

// TestCSIuReaderModifyOtherKeysMixed verifies mixed modifyOtherKeys + plain input.
func TestCSIuReaderModifyOtherKeysMixed(t *testing.T) {
	// "x" + Shift+N via modifyOtherKeys + "y"
	input := "x\x1b[27;2;78~y"
	r := NewCSIuReader(bytes.NewReader([]byte(input)))
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll error: %v", err)
	}
	want := "xNy"
	if string(out) != want {
		t.Errorf("CSIuReader mixed modifyOtherKeys: got %q, want %q", string(out), want)
	}
}

// TestCSIuReaderAllShiftHotkeys verifies every shift+letter hotkey used in
// agent-deck's TUI produces the correct uppercase byte through the reader.
// This is the integration-level test that would have caught the missing
// tea.WithInput(NewCSIuReader(os.Stdin)) wiring.
func TestCSIuReaderAllShiftHotkeys(t *testing.T) {
	// Every uppercase hotkey defined in defaultHotkeyBindings:
	//   N=quick_create, R=restart, D=close_session, M=move_to_group,
	//   F=fork_with_options, E=exec_shell, W=worktree_finish, S=settings,
	//   G=global_search, K=move_up, J=move_down, C=cost_dashboard
	hotkeys := map[rune]int{
		'N': 110, 'R': 114, 'D': 100, 'M': 109,
		'F': 102, 'E': 101, 'W': 119, 'S': 115,
		'G': 103, 'K': 107, 'J': 106, 'C': 99,
	}

	for want, codepoint := range hotkeys {
		t.Run("CSIu_Shift+"+string(want), func(t *testing.T) {
			// CSI u format: ESC[<codepoint>;2u (modifier 2 = shift)
			input := fmt.Sprintf("\x1b[%d;2u", codepoint)
			r := NewCSIuReader(bytes.NewReader([]byte(input)))
			out, err := io.ReadAll(r)
			if err != nil {
				t.Fatalf("ReadAll error: %v", err)
			}
			if string(out) != string(want) {
				t.Errorf("CSIuReader(%q) = %q, want %q", input, string(out), string(want))
			}
		})

		t.Run("modifyOtherKeys_Shift+"+string(want), func(t *testing.T) {
			// modifyOtherKeys format: ESC[27;2;<codepoint>~
			input := fmt.Sprintf("\x1b[27;2;%d~", codepoint)
			r := NewCSIuReader(bytes.NewReader([]byte(input)))
			out, err := io.ReadAll(r)
			if err != nil {
				t.Fatalf("ReadAll error: %v", err)
			}
			if string(out) != string(want) {
				t.Errorf("CSIuReader(%q) = %q, want %q", input, string(out), string(want))
			}
		})
	}
}

// TestCSIuReaderNonModifiedPassthrough verifies that standard escape sequences
// (arrows, Page Up/Down, function keys) pass through untouched when mixed with
// CSI u sequences.
func TestCSIuReaderNonModifiedPassthrough(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"up arrow", "\x1b[A", "\x1b[A"},
		{"down arrow", "\x1b[B", "\x1b[B"},
		{"right arrow", "\x1b[C", "\x1b[C"},
		{"left arrow", "\x1b[D", "\x1b[D"},
		{"home", "\x1b[H", "\x1b[H"},
		{"end", "\x1b[F", "\x1b[F"},
		{"page up", "\x1b[5~", "\x1b[5~"},
		{"page down", "\x1b[6~", "\x1b[6~"},
		{"delete", "\x1b[3~", "\x1b[3~"},
		{"F1", "\x1b[11~", "\x1b[11~"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewCSIuReader(bytes.NewReader([]byte(tt.input)))
			out, err := io.ReadAll(r)
			if err != nil {
				t.Fatalf("ReadAll error: %v", err)
			}
			if string(out) != tt.want {
				t.Errorf("CSIuReader(%q) = %q, want %q (should pass through)", tt.input, string(out), tt.want)
			}
		})
	}
}

// TestCSIuReaderRealWorldGhosttyTmux simulates the actual byte stream from
// Ghostty + tmux with extended-keys on. This is the scenario that triggered
// the original bug where shift keys were silently dropped.
func TestCSIuReaderRealWorldGhosttyTmux(t *testing.T) {
	// Simulated keystroke sequence: user navigates down (j), presses Shift+S
	// (Settings), then ESC to close. In tmux with extended-keys, Shift+S
	// arrives as CSI u.
	input := "j\x1b[115;2u\x1b"
	r := NewCSIuReader(bytes.NewReader([]byte(input)))
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll error: %v", err)
	}
	want := "jS\x1b"
	if string(out) != want {
		t.Errorf("real-world Ghostty+tmux: got %q, want %q", string(out), want)
	}
}

// TestCSIuReaderMixedProtocols verifies the reader handles a stream containing
// both CSI u and modifyOtherKeys sequences interspersed with legacy bytes.
func TestCSIuReaderMixedProtocols(t *testing.T) {
	// Legacy 'n' + CSI u Shift+R + modifyOtherKeys Shift+S + legacy 'q'
	input := "n\x1b[114;2u\x1b[27;2;115~q"
	r := NewCSIuReader(bytes.NewReader([]byte(input)))
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll error: %v", err)
	}
	want := "nRSq"
	if string(out) != want {
		t.Errorf("mixed protocols: got %q, want %q", string(out), want)
	}
}

func TestCSIuReaderBuffersSplitCSIuSequence(t *testing.T) {
	r := NewCSIuReader(&chunkedReader{
		chunks: [][]byte{
			[]byte("\x1b[115;"),
			[]byte("2u"),
		},
	})

	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll error: %v", err)
	}
	if string(out) != "S" {
		t.Errorf("split CSI u sequence: got %q, want %q", string(out), "S")
	}
}

func TestCSIuReaderBuffersSplitModifyOtherKeysSequence(t *testing.T) {
	r := NewCSIuReader(&chunkedReader{
		chunks: [][]byte{
			[]byte("\x1b[27;2;"),
			[]byte("83~"),
		},
	})

	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll error: %v", err)
	}
	if string(out) != "S" {
		t.Errorf("split modifyOtherKeys sequence: got %q, want %q", string(out), "S")
	}
}

func TestCSIuReaderDropsTerminalRepliesDuringQuarantine(t *testing.T) {
	t.Cleanup(termreply.Clear)
	termreply.QuarantineFor(time.Second)

	tests := []struct {
		name  string
		input string
	}{
		{
			name:  "drops OSC color reply",
			input: "\x1b]11;rgb:d3d3/f5f5/f5f5\x07",
		},
		{
			name:  "drops DCS kitty version reply",
			input: "\x1bP>|kitty(0.44.0)\x1b\\",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewCSIuReader(bytes.NewReader([]byte(tt.input)))
			out, err := io.ReadAll(r)
			if err != nil {
				t.Fatalf("ReadAll error: %v", err)
			}
			if string(out) != "" {
				t.Fatalf("expected terminal reply to be discarded, got %q", string(out))
			}
		})
	}
}

func TestCSIuReaderDropsSplitTerminalRepliesDuringQuarantine(t *testing.T) {
	t.Cleanup(termreply.Clear)
	termreply.QuarantineFor(time.Second)

	r := NewCSIuReader(&chunkedReader{
		chunks: [][]byte{
			[]byte("\x1bP>|kitty"),
			[]byte("(0.44.0)\x1b\\"),
			[]byte("\x1b]11;rgb:d3d3/f5f5"),
			[]byte("/d3d3/f5f5\x07"),
			[]byte("j"),
		},
	})

	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll error: %v", err)
	}
	if string(out) != "j" {
		t.Fatalf("expected split terminal replies to be discarded, got %q", string(out))
	}
}

func TestCSIuReaderPreservesNormalInputDuringQuarantine(t *testing.T) {
	t.Cleanup(termreply.Clear)
	termreply.QuarantineFor(time.Second)

	r := NewCSIuReader(bytes.NewReader([]byte("j\r")))
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll error: %v", err)
	}
	if string(out) != "j\r" {
		t.Fatalf("expected normal input to survive quarantine, got %q", string(out))
	}
}

func TestCSIuReaderBuffersSplitStandardCSISequence(t *testing.T) {
	r := NewCSIuReader(&chunkedReader{
		chunks: [][]byte{
			[]byte("\x1b["),
			[]byte("A"),
		},
	})

	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll error: %v", err)
	}
	if string(out) != "\x1b[A" {
		t.Errorf("split standard CSI sequence: got %q, want %q", string(out), "\x1b[A")
	}
}

// TestParseCSIuAllShiftLetters verifies ParseCSIu handles Shift+ for every
// letter a-z, not just the three originally tested.
func TestParseCSIuAllShiftLetters(t *testing.T) {
	for c := 'a'; c <= 'z'; c++ {
		input := fmt.Sprintf("\x1b[%d;2u", c)
		result := ParseCSIu([]byte(input))
		if result == nil {
			t.Errorf("ParseCSIu(%q) = nil for Shift+%c", input, c)
			continue
		}
		want := c - 'a' + 'A'
		if len(result.Runes) != 1 || result.Runes[0] != want {
			t.Errorf("ParseCSIu(%q) = %v, want [%c]", input, result.Runes, want)
		}
	}
}

// TestParseModifyOtherKeysAllShiftLetters verifies ParseModifyOtherKeys
// handles Shift+ for every letter a-z.
func TestParseModifyOtherKeysAllShiftLetters(t *testing.T) {
	for c := 'a'; c <= 'z'; c++ {
		input := fmt.Sprintf("\x1b[27;2;%d~", c)
		result := ParseModifyOtherKeys([]byte(input))
		if result == nil {
			t.Errorf("ParseModifyOtherKeys(%q) = nil for Shift+%c", input, c)
			continue
		}
		want := c - 'a' + 'A'
		if len(result.Runes) != 1 || result.Runes[0] != want {
			t.Errorf("ParseModifyOtherKeys(%q) = %v, want [%c]", input, result.Runes, want)
		}
	}
}

// TestCSIuReader_Underscore verifies that the CSIuReader translates the CSI u
// encoding for underscore (codepoint 95 with shift modifier) to a literal '_'
// byte (regression test for BUG-02).
func TestCSIuReader_Underscore(t *testing.T) {
	input := "\x1b[95;2u"
	r := NewCSIuReader(bytes.NewReader([]byte(input)))
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll error: %v", err)
	}
	if string(out) != "_" {
		t.Errorf("CSIuReader translated %q to %q, want %q", input, string(out), "_")
	}
}
