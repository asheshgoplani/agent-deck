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
// The source for Claude, and what was rejected: the JSON Claude Code pipes to a
// configured statusLine command, whose `rate_limits` block is documented
// (code.claude.com/docs/en/statusline). Every other candidate on this machine
// was checked on 2026-09-07 and does not carry the numbers: none of the 31 hook
// events, no `claude usage` subcommand, no OTel metric, no file under
// ~/.claude. The undocumented oauth usage endpoint some tools call is
// deliberately not used — it needs a bearer token replayed out of the user's
// credential store, and this package never reads a credential store.
//
// Nothing here derives a percentage from token counts. A locally computed
// estimate is a different number that happens to share a unit with the one the
// user is asking about, and being wrong about a quota is worse than not showing
// one.
//
// The model, the store and the CLI are deliberately shaped for more than one
// provider even though Claude is the only one wired up here: the store is
// one-file-per-provider and the report is a list, so a second provider is a new
// file rather than a change to this contract.
package quota

// Provider IDs. These are the on-disk filenames in the cache and the stable
// keys in `agent-deck usage --json`, so they are part of the contract and are
// spelled once here rather than as literals at each call site.
const (
	ProviderClaude = "claude"
)

// WindowKind names a quota window. It is a named kind rather than a bare map
// key because the kind is part of the `--json` contract: a consumer selects the
// five-hour window by name, without having to parse the human label.
type WindowKind string

const (
	WindowFiveHour   WindowKind = "five_hour"
	WindowSevenDay   WindowKind = "seven_day"
	WindowSpendLimit WindowKind = "spend_limit"
)

// Window is one quota window as the provider reported it.
type Window struct {
	Kind WindowKind `json:"kind"`
	// Label is the short human rendering ("5h", "7d", "spend"). It is carried
	// rather than derived from Kind so a provider whose window this build
	// cannot name in advance can still label itself from its own numbers.
	Label string `json:"label"`
	// UsedPercentage is percent CONSUMED, 0-100 — except that Claude's
	// spend_limit exceeds 100 in overage. It is NOT clamped: reporting a
	// breached limit as an exactly-met one hides the very thing the user
	// opened this to see.
	UsedPercentage float64 `json:"used_percentage"`
	// ResetsAt is epoch SECONDS, or nil when the provider said nothing.
	//
	// The pointer is the whole point: Claude omits the reset for a window it
	// has not populated. A zero would render as a window that reset in 1970,
	// and a synthesised "now + 5h" would be a promise this package is in no
	// position to make. Never fill this in from the window's nominal duration.
	ResetsAt *int64 `json:"resets_at,omitempty"`
}

// Snapshot is one provider's quota state as of UpdatedAt.
type Snapshot struct {
	ID        string   `json:"id"`
	Label     string   `json:"label"`
	Windows   []Window `json:"windows"`
	UpdatedAt int64    `json:"updated_at"`
	// Error is a short, provider-attributed message. A provider that failed is
	// reported as data alongside the ones that succeeded — one unreadable
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
