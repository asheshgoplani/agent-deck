package events

import "testing"

func TestIsMacappBuiltinKind(t *testing.T) {
	for _, kind := range []string{KindMacappOpen, KindMacappOpenAck, KindMacappBrowserRequest, KindMacappBrowserResponse, KindMacappAppFocus, KindMacappSessionClosed} {
		if !IsMacappBuiltinKind(kind) {
			t.Errorf("%s gated", kind)
		}
	}
	// Plugin kinds, including the local browser plugin kinds, stay gated.
	for _, kind := range []string{"macapp.browser.open", "macapp.browser.reload", "macapp.browser", "macapp.open.other", "macapp.canvas.show", "macapp.", "session.status", ""} {
		if IsMacappBuiltinKind(kind) {
			t.Errorf("%q admitted as built-in", kind)
		}
	}
}

func TestNewIDIsRandomHex(t *testing.T) {
	a, b := NewID(), NewID()
	if len(a) != 16 || a == b {
		t.Fatalf("ids %q %q", a, b)
	}
	for _, c := range a {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			t.Fatalf("not hex: %q", a)
		}
	}
}
