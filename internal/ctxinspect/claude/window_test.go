package claude

import (
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/ctxinspect"
)

func TestResolveWindowPrecedence(t *testing.T) {
	tests := []struct {
		name       string
		model      string
		env        map[string]string
		wantTokens int
		wantSource ctxinspect.WindowSource
	}{
		{
			name:       "environment override wins over the model table",
			model:      "claude-opus-4",
			env:        map[string]string{contextWindowEnv: "123456"},
			wantTokens: 123456,
			wantSource: ctxinspect.WindowEnvOverride,
		},
		{
			name:       "unparsable override falls through",
			model:      "claude-opus-4",
			env:        map[string]string{contextWindowEnv: "lots"},
			wantTokens: 200000,
			wantSource: ctxinspect.WindowModelDefault,
		},
		{
			name:       "non-positive override falls through",
			model:      "claude-opus-4",
			env:        map[string]string{contextWindowEnv: "0"},
			wantTokens: 200000,
			wantSource: ctxinspect.WindowModelDefault,
		},
		{
			// The suffix is part of the identifier the harness recorded, so it
			// is evidence rather than inference.
			name:       "long-context suffix is harness-reported",
			model:      "claude-opus-5[1m]",
			wantTokens: 1000000,
			wantSource: ctxinspect.WindowHarnessReported,
		},
		{
			name:       "most specific prefix wins",
			model:      "claude-opus-4-7-20260101",
			wantTokens: 1000000,
			wantSource: ctxinspect.WindowModelDefault,
		},
		{
			// A long trailing number is a release date, not a minor version.
			name:       "dated release uses the family default",
			model:      "claude-sonnet-4-20250514",
			wantTokens: 200000,
			wantSource: ctxinspect.WindowModelDefault,
		},
		{
			// A word after the family is a variant, not a minor version.
			name:       "family variant uses the family default",
			model:      "claude-3-5-sonnet-20241022",
			wantTokens: 200000,
			wantSource: ctxinspect.WindowModelDefault,
		},
		{
			// The stale-table trap, answered rather than dead-ended: an unseen
			// minor version takes the window of the closest taught release at
			// or below it — claude-opus-4-8's 1M, not claude-opus-4's 200,000 —
			// and is marked assumed so no surface can present it as measured.
			// Answering 200,000 here would have reported the session as five
			// times fuller than it is.
			name:       "unseen minor version assumes the closest taught release",
			model:      "claude-opus-4-9",
			wantTokens: 1000000,
			wantSource: ctxinspect.WindowModelFamily,
		},
		{
			// The interpolation runs downwards too: 4.2 sits between taught
			// 4.1 and taught 4.5, and must not inherit 4.8's 1M window.
			name:       "unseen minor version below the 1M releases keeps 200k",
			model:      "claude-opus-4-2",
			wantTokens: 200000,
			wantSource: ctxinspect.WindowModelFamily,
		},
		{
			// A two-digit minor must not be read as the one-digit release
			// whose text it opens with: "claude-opus-4-10" begins with
			// "claude-opus-4-1", and a bare prefix match handed it 200,000.
			name:       "two-digit minor is not the one-digit release it starts with",
			model:      "claude-opus-4-10",
			wantTokens: 1000000,
			wantSource: ctxinspect.WindowModelFamily,
		},
		{
			// The half the fleet actually hit: 44 live sessions read "window
			// unknown" because their model was real, current, and simply
			// absent from this table. A shipped release must resolve exactly,
			// not by assumption.
			name:       "current Opus is taught, not refused",
			model:      "claude-opus-5",
			wantTokens: 1000000,
			wantSource: ctxinspect.WindowModelDefault,
		},
		{
			name:       "Fable is taught",
			model:      "claude-fable-5",
			wantTokens: 1000000,
			wantSource: ctxinspect.WindowModelDefault,
		},
		{
			name:       "Opus 4.8 is taught",
			model:      "claude-opus-4-8",
			wantTokens: 1000000,
			wantSource: ctxinspect.WindowModelDefault,
		},
		{
			name:       "Sonnet 5 is taught",
			model:      "claude-sonnet-5",
			wantTokens: 1000000,
			wantSource: ctxinspect.WindowModelDefault,
		},
		{
			// A released 200k minor version must not be swallowed by the
			// family guard either: haiku-4-5 is real and is not 4.
			name:       "released 200k minor version resolves",
			model:      "claude-haiku-4-5",
			wantTokens: 200000,
			wantSource: ctxinspect.WindowModelDefault,
		},
		{
			name:       "Sonnet 4.5 resolves",
			model:      "claude-sonnet-4-5-20250929",
			wantTokens: 200000,
			wantSource: ctxinspect.WindowModelDefault,
		},
		{
			// The refusal that survives: an unrecognised FAMILY has nothing to
			// interpolate from, so it yields no denominator rather than a
			// plausible wrong one.
			name:       "unknown model is unknown, not defaulted",
			model:      "claude-something-9",
			wantTokens: 0,
			wantSource: ctxinspect.WindowUnknown,
		},
		{
			name:       "no model at all",
			model:      "",
			wantTokens: 0,
			wantSource: ctxinspect.WindowUnknown,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := emptyEnv
			if tc.env != nil {
				env = envMap(tc.env)
			}
			got := ResolveWindow(tc.model, env)
			if got.Tokens != tc.wantTokens {
				t.Errorf("Tokens = %d, want %d", got.Tokens, tc.wantTokens)
			}
			if got.Source != tc.wantSource {
				t.Errorf("Source = %s, want %s", got.Source, tc.wantSource)
			}
			if got.Detail == "" {
				t.Error("want the origin of the figure named, so a user can check it")
			}
			if got.Known() != (tc.wantTokens > 0) {
				t.Errorf("Known() = %v, want %v", got.Known(), tc.wantTokens > 0)
			}
		})
	}
}

func TestHasUnknownMinorVersion(t *testing.T) {
	tests := []struct {
		id, family string
		want       bool
	}{
		{"claude-opus-4-8", "claude-opus-4", true},
		{"claude-opus-4-12", "claude-opus-4", true},
		{"claude-opus-4", "claude-opus-4", false},
		{"claude-opus-4-20250514", "claude-opus-4", false},
		{"claude-3-5-sonnet-20241022", "claude-3-5", false},
		{"claude-opus-4-latest", "claude-opus-4", false},
	}
	for _, tc := range tests {
		if got := hasUnknownMinorVersion(tc.id, tc.family); got != tc.want {
			t.Errorf("hasUnknownMinorVersion(%q, %q) = %v, want %v", tc.id, tc.family, got, tc.want)
		}
	}
}

func TestMatchesPrefix(t *testing.T) {
	tests := []struct {
		id, prefix string
		want       bool
	}{
		{"claude-opus-4-1", "claude-opus-4-1", true},
		{"claude-opus-4-1-20250805", "claude-opus-4-1", true},
		{"claude-opus-5[1m]", "claude-opus-5", true},
		// The whole reason this helper exists.
		{"claude-opus-4-10", "claude-opus-4-1", false},
		{"claude-sonnet-45", "claude-sonnet-4", false},
	}
	for _, tc := range tests {
		if got := matchesPrefix(tc.id, tc.prefix); got != tc.want {
			t.Errorf("matchesPrefix(%q, %q) = %v, want %v", tc.id, tc.prefix, got, tc.want)
		}
	}
}

// An assumed window must be visible as an assumption everywhere it travels: a
// figure that reads as measured is the failure this whole feature exists to
// prevent, and Assumed is the only thing standing between the two.
func TestResolveWindowMarksAnAssumption(t *testing.T) {
	assumed := ResolveWindow("claude-opus-4-9", emptyEnv)
	if !assumed.Assumed() {
		t.Fatalf("Assumed() = false for %+v, want true", assumed)
	}
	// Detail is provenance, not advice: it must name the release the figure
	// was borrowed from. The remedy is ctxtext's job on every surface.
	if !strings.Contains(assumed.Detail, "claude-opus-4-8") {
		t.Errorf("Detail = %q, want it to name the release the window came from", assumed.Detail)
	}

	taught := ResolveWindow("claude-opus-4-8", emptyEnv)
	if taught.Assumed() {
		t.Errorf("a taught release must not be marked assumed: %+v", taught)
	}
	// The honest refusal survives: a family with nothing to interpolate from
	// still yields no denominator rather than a plausible one.
	unknown := ResolveWindow("claude-newthing-2", emptyEnv)
	if unknown.Known() || unknown.Assumed() {
		t.Errorf("an unknown family must stay unknown: %+v", unknown)
	}
}

func TestResolveWindowNilEnv(t *testing.T) {
	// A nil probe must not panic; it simply means no override was supplied.
	if got := ResolveWindow("claude-opus-4", nil); got.Tokens != 200000 {
		t.Fatalf("Tokens = %d, want the model default", got.Tokens)
	}
}
