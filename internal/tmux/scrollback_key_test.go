//go:build !windows
// +build !windows

package tmux

import "testing"

// TestIndexScrollbackTrigger_PageUp verifies a bare PageUp is detected only when
// ScrollbackOnPageUp is set, and that modified PageUp variants never match.
func TestIndexScrollbackTrigger_PageUp(t *testing.T) {
	tests := []struct {
		name string
		data string
		opts AttachOptions
		want int
	}{
		{
			name: "bare pageup detected when enabled",
			data: "\x1b[5~",
			opts: AttachOptions{ScrollbackOnPageUp: true},
			want: 0,
		},
		{
			name: "bare pageup ignored when disabled",
			data: "\x1b[5~",
			opts: AttachOptions{},
			want: -1,
		},
		{
			name: "shift+pageup (ESC[5;2~) passes through",
			data: "\x1b[5;2~",
			opts: AttachOptions{ScrollbackOnPageUp: true},
			want: -1,
		},
		{
			name: "ctrl+pageup (ESC[5;5~) passes through",
			data: "\x1b[5;5~",
			opts: AttachOptions{ScrollbackOnPageUp: true},
			want: -1,
		},
		{
			name: "pageup mid-buffer detected at its offset",
			data: "abc\x1b[5~",
			opts: AttachOptions{ScrollbackOnPageUp: true},
			want: 3,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := indexScrollbackTrigger([]byte(tt.data), tt.opts); got != tt.want {
				t.Fatalf("indexScrollbackTrigger(%q) = %d, want %d", tt.data, got, tt.want)
			}
		})
	}
}

// TestIndexScrollbackTrigger_CtrlByte verifies the control-byte trigger is
// detected across the raw, modifyOtherKeys, and CSI-u encodings, and that the
// earliest of two configured triggers wins.
func TestIndexScrollbackTrigger_CtrlByte(t *testing.T) {
	const ctrlG = byte(7) // Ctrl+G

	// Raw byte.
	if got := indexScrollbackTrigger([]byte{ctrlG}, AttachOptions{ScrollbackKeyByte: ctrlG}); got != 0 {
		t.Fatalf("raw ctrl+g: got %d, want 0", got)
	}
	// modifyOtherKeys encoding: ESC[27;5;103~ ('g' == 103).
	if got := indexScrollbackTrigger([]byte("\x1b[27;5;103~"), AttachOptions{ScrollbackKeyByte: ctrlG}); got != 0 {
		t.Fatalf("modifyOtherKeys ctrl+g: got %d, want 0", got)
	}
	// CSI-u (kitty) encoding: ESC[103;5u.
	if got := indexScrollbackTrigger([]byte("\x1b[103;5u"), AttachOptions{ScrollbackKeyByte: ctrlG}); got != 0 {
		t.Fatalf("CSI-u ctrl+g: got %d, want 0", got)
	}
	// Disabled by default.
	if got := indexScrollbackTrigger([]byte{ctrlG}, AttachOptions{}); got != -1 {
		t.Fatalf("ctrl+g without trigger: got %d, want -1", got)
	}
	// Earliest of two triggers wins: pageup at 0, ctrl+g at 4.
	opts := AttachOptions{ScrollbackKeyByte: ctrlG, ScrollbackOnPageUp: true}
	if got := indexScrollbackTrigger([]byte("\x1b[5~\x07"), opts); got != 0 {
		t.Fatalf("earliest trigger: got %d, want 0", got)
	}
}

// TestIndexScrollbackTrigger_PageUpGate verifies that ScrollbackGate suppresses
// ONLY the bare-PageUp trigger (leaving the key for the attached program), never
// the explicit control-byte chord, and that a nil gate preserves the legacy
// always-open behaviour. This is the alternate-screen passthrough fix: when the
// pane is a full-screen app (Claude fullscreen), PageUp must reach the app.
func TestIndexScrollbackTrigger_PageUpGate(t *testing.T) {
	const ctrlG = byte(7) // Ctrl+G
	gateTrue := func() bool { return true }
	gateFalse := func() bool { return false }

	tests := []struct {
		name string
		data string
		opts AttachOptions
		want int
	}{
		{
			name: "gate open => pageup detected",
			data: "\x1b[5~",
			opts: AttachOptions{ScrollbackOnPageUp: true, ScrollbackGate: gateTrue},
			want: 0,
		},
		{
			name: "gate closed => pageup suppressed (passthrough)",
			data: "\x1b[5~",
			opts: AttachOptions{ScrollbackOnPageUp: true, ScrollbackGate: gateFalse},
			want: -1,
		},
		{
			name: "nil gate => pageup detected (legacy)",
			data: "\x1b[5~",
			opts: AttachOptions{ScrollbackOnPageUp: true},
			want: 0,
		},
		{
			name: "gate closed never suppresses the ctrl-byte chord",
			data: "\x07",
			opts: AttachOptions{ScrollbackKeyByte: ctrlG, ScrollbackGate: gateFalse},
			want: 0,
		},
		{
			name: "gate closed suppresses pageup but chord still wins",
			data: "\x1b[5~\x07",
			opts: AttachOptions{ScrollbackKeyByte: ctrlG, ScrollbackOnPageUp: true, ScrollbackGate: gateFalse},
			want: 4,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := indexScrollbackTrigger([]byte(tt.data), tt.opts); got != tt.want {
				t.Fatalf("indexScrollbackTrigger(%q) = %d, want %d", tt.data, got, tt.want)
			}
		})
	}
}

// TestIndexScrollbackTrigger_GateOnlyOnPageUp verifies the gate is consulted only
// when a bare PageUp is actually present — never on ordinary keystrokes — so the
// per-press tmux query it performs cannot run on every input chunk.
func TestIndexScrollbackTrigger_GateOnlyOnPageUp(t *testing.T) {
	calls := 0
	opts := AttachOptions{ScrollbackOnPageUp: true, ScrollbackGate: func() bool {
		calls++
		return true
	}}
	// Ordinary input with no PageUp: the gate must not be consulted.
	indexScrollbackTrigger([]byte("hello world"), opts)
	if calls != 0 {
		t.Fatalf("gate consulted %d times on non-PageUp input, want 0", calls)
	}
	// A PageUp present: gate consulted exactly once.
	indexScrollbackTrigger([]byte("\x1b[5~"), opts)
	if calls != 1 {
		t.Fatalf("gate consulted %d times on PageUp input, want 1", calls)
	}
}

// TestResolveAttachInterrupt_Precedence verifies detach > switch > scrollback
// precedence and earliest-index-wins semantics.
func TestResolveAttachInterrupt_Precedence(t *testing.T) {
	const (
		detach = byte(17) // Ctrl+Q
		swByte = byte(19) // Ctrl+S
		sbByte = byte(7)  // Ctrl+G
	)
	opts := AttachOptions{SwitchKeyByte: swByte, ScrollbackKeyByte: sbByte, ScrollbackOnPageUp: true}

	tests := []struct {
		name     string
		data     string
		wantIdx  int
		wantKind SwitchIntent
	}{
		{"nothing", "hello", -1, SwitchNone},
		{"detach only", "\x11", 0, SwitchNone},
		{"switch only", "\x13", 0, SwitchRequested},
		{"scrollback pageup only", "\x1b[5~", 0, ScrollbackRequested},
		{"scrollback ctrl byte only", "\x07", 0, ScrollbackRequested},
		// Detach earlier than scrollback => detach wins.
		{"detach before scrollback", "\x11\x1b[5~", 0, SwitchNone},
		// Scrollback earlier than detach => scrollback wins (earliest index).
		{"scrollback before detach", "\x1b[5~\x11", 0, ScrollbackRequested},
		// Switch and scrollback: earliest wins; switch earlier here.
		{"switch before scrollback", "\x13\x07", 0, SwitchRequested},
		{"scrollback before switch", "\x07\x13", 0, ScrollbackRequested},
		// Bytes before the trigger shift the index.
		{"prefix then scrollback", "abc\x07", 3, ScrollbackRequested},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotIdx, gotKind := resolveAttachInterrupt([]byte(tt.data), detach, opts)
			if gotIdx != tt.wantIdx || gotKind != tt.wantKind {
				t.Fatalf("resolveAttachInterrupt(%q) = (%d, %v), want (%d, %v)",
					tt.data, gotIdx, gotKind, tt.wantIdx, tt.wantKind)
			}
		})
	}
}

// TestResolveAttachInterrupt_LoneScrollback confirms a lone scrollback trigger
// resolves to ScrollbackRequested even when the (absent) detach key is
// configured — the detach key does not shadow scrollback when detach is not
// actually present in the chunk.
func TestResolveAttachInterrupt_LoneScrollback(t *testing.T) {
	opts := AttachOptions{ScrollbackOnPageUp: true}
	idx, kind := resolveAttachInterrupt([]byte("\x1b[5~"), 17, opts)
	if idx != 0 || kind != ScrollbackRequested {
		t.Fatalf("lone scrollback: got (%d,%v), want (0,ScrollbackRequested)", idx, kind)
	}
}
