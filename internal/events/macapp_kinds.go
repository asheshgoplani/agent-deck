package events

// Built-in Mac app kinds (docs/macapp-open.md, remote browser contract).
// They carry the box CLI <-> Mac app protocol and are published without the
// [macapp] plugins opt-in; every other macapp.* kind is a plugin kind and
// stays gated.
const (
	KindMacappOpen            = "macapp.open"
	KindMacappOpenAck         = "macapp.open.ack"
	KindMacappBrowserRequest  = "macapp.browser.request"
	KindMacappBrowserResponse = "macapp.browser.response"
	KindMacappAppFocus        = "macapp.app.focus"
	KindMacappSessionClosed   = "macapp.session.closed"
)

// IsMacappBuiltinKind reports whether kind is one of the built-in Mac app
// kinds that `events publish` admits without [macapp] plugins = true.
func IsMacappBuiltinKind(kind string) bool {
	switch kind {
	case KindMacappOpen, KindMacappOpenAck, KindMacappBrowserRequest, KindMacappBrowserResponse, KindMacappAppFocus, KindMacappSessionClosed:
		return true
	}
	return false
}

// NewID returns a random hex id (the same generator as frame event ids),
// for request correlation such as macapp.open request_id.
func NewID() string { return genEventID() }
