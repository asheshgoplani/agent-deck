package events

// Built-in Mac app kinds (docs/macapp-open.md, remote browser contract).
// Every other macapp.* kind is a plugin kind.
const (
	KindMacappOpen            = "macapp.open"
	KindMacappOpenAck         = "macapp.open.ack"
	KindMacappBrowserRequest  = "macapp.browser.request"
	KindMacappBrowserResponse = "macapp.browser.response"
	KindMacappAppFocus        = "macapp.app.focus"
	KindMacappSessionClosed   = "macapp.session.closed"
)

// IsMacappUngatedKind reports whether `events publish` admits kind without
// [macapp] plugins = true. Only the Mac app's own frames are ungated: the
// open request and ack, and the app's browser replies and focus claim. A
// macapp.browser.request drives the owner's browser and stays behind the
// opt-in, like every plugin kind; macapp.session.closed is published by the
// host itself, never from the CLI without the opt-in.
func IsMacappUngatedKind(kind string) bool {
	switch kind {
	case KindMacappOpen, KindMacappOpenAck, KindMacappBrowserResponse, KindMacappAppFocus:
		return true
	}
	return false
}

// IsMacappRemoteReplyKind reports whether kind is one of the Mac app's
// replies, the only kinds `remote <name> events publish` forwards to a box.
func IsMacappRemoteReplyKind(kind string) bool {
	switch kind {
	case KindMacappOpenAck, KindMacappBrowserResponse, KindMacappAppFocus:
		return true
	}
	return false
}

// NewID returns a random hex id (the same generator as frame event ids),
// for request correlation such as macapp.open request_id.
func NewID() string { return genEventID() }
