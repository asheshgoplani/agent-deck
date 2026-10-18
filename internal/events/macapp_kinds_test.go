package events

import "testing"

func TestIsMacappUngatedKind(t *testing.T) {
	for _, kind := range []string{KindMacappOpen, KindMacappOpenAck, KindMacappBrowserResponse, KindMacappAppFocus} {
		if !IsMacappUngatedKind(kind) {
			t.Errorf("%s gated", kind)
		}
	}
	// A browser request drives the owner's browser: it needs the opt-in, as
	// do plugin kinds (including the local browser plugin kinds) and the
	// host's own session.closed.
	for _, kind := range []string{KindMacappBrowserRequest, KindMacappSessionClosed, "macapp.browser.open", "macapp.browser.reload", "macapp.browser", "macapp.open.other", "macapp.canvas.show", "macapp.", "session.status", ""} {
		if IsMacappUngatedKind(kind) {
			t.Errorf("%q admitted without the opt-in", kind)
		}
	}
}

func TestIsMacappRemoteReplyKind(t *testing.T) {
	for _, kind := range []string{KindMacappOpenAck, KindMacappBrowserResponse, KindMacappAppFocus} {
		if !IsMacappRemoteReplyKind(kind) {
			t.Errorf("%s not a reply", kind)
		}
	}
	for _, kind := range []string{KindMacappOpen, KindMacappBrowserRequest, KindMacappSessionClosed, "macapp.browser.response.x", ""} {
		if IsMacappRemoteReplyKind(kind) {
			t.Errorf("%q forwarded as a reply", kind)
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
