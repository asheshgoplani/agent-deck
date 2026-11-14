package tmux

import "testing"

func TestListingProvesAbsent(t *testing.T) {
	listed := map[string]struct{}{"agentdeck_web_1a2b": {}, "agentdeck_api-long": {}}
	for _, c := range []struct {
		name   string
		listed map[string]struct{}
		want   bool
	}{
		{"agentdeck_gone_9f9f", listed, true},
		{"agentdeck_gone_9f9f", map[string]struct{}{}, true}, // empty server
		{"agentdeck_web_1a2b", listed, false},                // listed
		{"agentdeck_api", listed, false},                     // prefix of a listed session
		{"agentdeck_gone_9f9f", nil, false},                  // listing failed
		{"", listed, false},
		{"agentdeck_w*", listed, false}, // glob
		{"agentdeck_x:1", listed, false},
		{"agentdeck_x.1", listed, false},
		{"$3", listed, false},
		{"@3", listed, false},
		{"%3", listed, false},
		{"=agentdeck_web_1a2b", listed, false}, // exact-match target syntax
	} {
		if got := listingProvesAbsent(c.listed, c.name); got != c.want {
			t.Errorf("listingProvesAbsent(%v, %q) = %v, want %v", c.listed, c.name, got, c.want)
		}
	}
}
