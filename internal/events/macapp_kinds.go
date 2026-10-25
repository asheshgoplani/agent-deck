package events

import "strings"

// Built-in Mac app kinds (docs/macapp-open.md, remote browser contract).
// Every other macapp.* kind is a plugin kind.
const (
	KindMacappOpen            = "macapp.open"
	KindMacappOpenAck         = "macapp.open.ack"
	KindMacappBrowserRequest  = "macapp.browser.request"
	KindMacappBrowserResponse = "macapp.browser.response"
	KindMacappAppFocus        = "macapp.app.focus"
	KindMacappSessionClosed   = "macapp.session.closed"

	// Ask the owner (docs/ask.md). macapp.ask and macapp.ask.ack are
	// published only by the host's own `agent-deck ask` (IsMacappHostOnlyKind);
	// macapp.ask.seen is a UI's frame.
	KindMacappAsk     = "macapp.ask"
	KindMacappAskSeen = "macapp.ask.seen"
	KindMacappAskAck  = "macapp.ask.ack"
)

// IsMacappUngatedKind reports whether `events publish` admits kind without
// [macapp] plugins = true. Only the Mac app's own frames are ungated: the
// open request and ack, the app's browser replies and focus claim, and the
// ask dialog's seen frame. A
// macapp.browser.request drives the owner's browser and stays behind the
// opt-in, like every plugin kind; macapp.session.closed is published by the
// host itself, never from the CLI without the opt-in.
func IsMacappUngatedKind(kind string) bool {
	switch kind {
	case KindMacappOpen, KindMacappOpenAck, KindMacappBrowserResponse, KindMacappAppFocus, KindMacappAskSeen:
		return true
	}
	return false
}

// IsMacappHostOnlyKind reports whether kind may be published only by the
// host's own `agent-deck ask` (in process) and never through `events
// publish`, whatever [macapp] plugins says: the ask request and its ack, and
// any other macapp.ask.* kind except the UI's seen frame. A forged ack would
// close the owner's dialog; a forged request would show one.
func IsMacappHostOnlyKind(kind string) bool {
	if kind == KindMacappAskSeen {
		return false
	}
	return kind == KindMacappAsk || strings.HasPrefix(kind, KindMacappAsk+".")
}

// IsMacappRemoteReplyKind reports whether kind is one of the Mac app's
// replies, the only kinds `remote <name> events publish` forwards to a box.
func IsMacappRemoteReplyKind(kind string) bool {
	switch kind {
	case KindMacappOpenAck, KindMacappBrowserResponse, KindMacappAppFocus, KindMacappAskSeen:
		return true
	}
	return false
}

// NewID returns a random hex id (the same generator as frame event ids),
// for request correlation such as macapp.open request_id.
func NewID() string { return genEventID() }
