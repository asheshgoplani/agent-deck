package send

// Review finding B2 (red on 205b174d0d). tmux resolves key names
// case-insensitively (key-string.c uses strcasecmp) and accepts the "^X" and
// lower-case "c-x" control forms, so each of these reaches Claude as Enter,
// Tab or Right. The raw send-keys guard must treat them like their canonical
// spellings.

import "testing"

func TestReview_BareKeyActsOnSuggestion_TmuxSpellings(t *testing.T) {
	for _, k := range []string{
		"enter", "ENTER", "tab", "TAB", "right", "RIGHT",
		"c-m", "C-M", "^M", "^m", "c-i", "C-I", "^I", "kpenter",
	} {
		if !BareKeyActsOnSuggestion(k) {
			t.Errorf("%q reaches Claude as Enter/Tab/Right but is not guarded", k)
		}
	}
}
