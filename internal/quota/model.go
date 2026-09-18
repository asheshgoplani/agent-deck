// Package quota reports how much of a provider's SUBSCRIPTION allowance is
// left, from the provider's own numbers.
//
// This is the forward-looking half of a signal agent-deck already has one half
// of. internal/session/usagelimit.go detects that a plan window is exhausted,
// after the fact, from the rejection turn in Claude's transcript — it answers
// "this session is blocked". It cannot answer "how much is left", and it says
// so itself: its staleness bound carries a "KNOWN LIMITATION, accepted
// deliberately" note that it does not read the real reset time, so a rejection
// can be believed for up to ~5h after the window actually reopened. The
// provider-reported reset in this package is exactly that missing number.
// Rewiring usagelimit.go to consume it is deliberately NOT done here.
//
// Source selection, and what was rejected:
//
//   - Claude: the JSON Claude Code pipes to a configured statusLine command,
//     whose `rate_limits` block is documented (code.claude.com/docs/en/statusline).
//     Every other candidate on this machine was checked on 2026-09-07 and does
//     not carry the numbers: none of the 31 hook events, no `claude usage`
//     subcommand, no OTel metric, no file under ~/.claude. The undocumented
//     oauth usage endpoint some tools call is deliberately not used — it needs a
//     bearer token replayed out of the user's credential store, and this package
//     never reads a credential store.
//   - Z.ai / GLM: the same monitor endpoint the vendor's own coding plugin calls,
//     on the host the user themselves put in ANTHROPIC_BASE_URL. There is no
//     hardcoded fallback host: an outbound destination this package invents
//     rather than the user configuring it is the thing the repo's security lens
//     forbids.
//
// Nothing here derives a percentage from token counts. A locally computed
// estimate is a different number that happens to share a unit with the one the
// user is asking about, and being wrong about a quota is worse than not showing
// one.
package quota

// Provider IDs. These are the on-disk filenames in the cache and the stable
// keys in `agent-deck usage --json`, so they are part of the contract and are
// spelled once here rather than as literals at each call site.
const (
	ProviderClaude = "claude"
	ProviderZai    = "zai"
)

// WindowKind names a quota window. It is a named kind rather than a map key
// because Z.ai returns windows this package cannot name in advance: its windows
// are discriminated by a (unit, number) pair for which only two values have
// been observed, so anything else has to remain representable without being
// given a name it may not deserve. WindowOther is that honest fallback.
type WindowKind string

const (
	WindowFiveHour   WindowKind = "five_hour"
	WindowSevenDay   WindowKind = "seven_day"
	WindowSpendLimit WindowKind = "spend_limit"
	WindowOther      WindowKind = "other"
)

// Window is one quota window as the provider reported it.
type Window struct {
	Kind WindowKind `json:"kind"`
	// Label is the short human rendering ("5h", "7d", "spend"). It is carried
	// rather than derived from Kind because a WindowOther window's only useful
	// label is the one built from the provider's own numbers at parse time.
	Label string `json:"label"`
	// UsedPercentage is percent CONSUMED, 0-100 — except that Claude's
	// spend_limit exceeds 100 in overage. It is NOT clamped: reporting a
	// breached limit as an exactly-met one hides the very thing the user
	// opened this to see.
	UsedPercentage float64 `json:"used_percentage"`
	// ResetsAt is epoch SECONDS, or nil when the provider said nothing.
	//
	// The pointer is the whole point: Z.ai omits the reset for a window with no
	// consumption yet, and Claude omits it for a window it has not populated. A
	// zero would render as a window that reset in 1970, and a synthesised
	// "now + 5h" would be a promise this package is in no position to make.
	// Never fill this in from the window's nominal duration.
	ResetsAt *int64 `json:"resets_at,omitempty"`
}

// Snapshot is one provider's quota state as of UpdatedAt.
type Snapshot struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	// Plan is the provider's own name for the subscription tier, when it
	// reports one (Z.ai's data.level). Claude's statusLine does not carry the
	// plan name, so it stays empty rather than being guessed from the limits.
	Plan      string   `json:"plan,omitempty"`
	Windows   []Window `json:"windows"`
	UpdatedAt int64    `json:"updated_at"`
	// Error is a short, provider-attributed message. A provider that failed is
	// reported as data alongside the ones that succeeded — one unreachable
	// provider must never blank out the others, which is the whole reason a
	// failure lives in the model instead of being returned as an error.
	//
	// It must never carry a token, an account id, or a URL with credentials in
	// it: this field is persisted and printed.
	Error string `json:"error,omitempty"`
	// Stale is computed at READ time against the store's TTL and is never
	// persisted — a snapshot that was fresh when written would otherwise claim
	// to be fresh forever. A stale snapshot is shown, marked; hiding it would
	// leave the user with nothing where they previously had a number.
	Stale bool `json:"stale"`
}

// Report is the shape of `agent-deck usage --json`.
type Report struct {
	Providers []Snapshot `json:"providers"`
}
