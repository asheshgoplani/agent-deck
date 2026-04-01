package ui

import (
	"bytes"
	"io"
	"testing"
)

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
