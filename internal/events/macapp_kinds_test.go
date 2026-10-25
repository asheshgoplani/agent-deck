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

// ASK-117 gating (PLAN.md section 11/12): macapp.ask and macapp.ask.ack are
// published only by the host's own `agent-deck ask`, never admitted by
// `events publish`; macapp.ask.seen is a UI frame, ungated and forwarded as
// a remote reply.
func TestMacappAskKindGating(t *testing.T) {
	if KindMacappAsk != "macapp.ask" || KindMacappAskSeen != "macapp.ask.seen" || KindMacappAskAck != "macapp.ask.ack" {
		t.Fatalf("kind names %q %q %q", KindMacappAsk, KindMacappAskSeen, KindMacappAskAck)
	}
	if !IsMacappUngatedKind(KindMacappAskSeen) || !IsMacappRemoteReplyKind(KindMacappAskSeen) {
		t.Fatal("macapp.ask.seen must be ungated and a remote reply kind")
	}
	for _, kind := range []string{KindMacappAsk, KindMacappAskAck, "macapp.ask.other", "macapp.ask.ack.x"} {
		if IsMacappUngatedKind(kind) || IsMacappRemoteReplyKind(kind) {
			t.Errorf("%q admitted or forwarded", kind)
		}
		if !IsMacappHostOnlyKind(kind) {
			t.Errorf("%q is not host-only", kind)
		}
	}
	for _, kind := range []string{KindMacappAskSeen, KindMacappOpen, KindMacappOpenAck, "macapp.asking", "macapp.askx", ""} {
		if IsMacappHostOnlyKind(kind) {
			t.Errorf("%q is host-only", kind)
		}
	}
}
