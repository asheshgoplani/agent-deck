package main

import (
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestResolveStreamRemotes(t *testing.T) {
	cfg := &session.UserConfig{Remotes: map[string]session.RemoteConfig{"box": {}, "lab": {}}}
	cfg.Web.StreamRemotes = []string{"lab"}
	cases := []struct {
		flag, want, err string
	}{
		{"", "lab", ""},                          // config key
		{"box", "box", ""},                       // flag wins
		{" box , lab ", "box,lab", ""},           // trimmed list
		{"nope", "", "--stream-remotes: remote"}, // unknown in the flag
	}
	for _, c := range cases {
		got, err := resolveStreamRemotes(c.flag, cfg)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("flag %q: err %v, want %q", c.flag, err, c.err)
			}
			continue
		}
		if err != nil || strings.Join(got, ",") != c.want {
			t.Errorf("flag %q: %v %v, want %s", c.flag, got, err, c.want)
		}
	}
	cfg.Web.StreamRemotes = []string{"gone"}
	if _, err := resolveStreamRemotes("", cfg); err == nil || !strings.Contains(err.Error(), "[web] stream_remotes") {
		t.Errorf("unknown name in the config key: %v", err)
	}
	if got, err := resolveStreamRemotes("", &session.UserConfig{}); err != nil || len(got) != 0 {
		t.Errorf("nothing configured: %v %v", got, err)
	}
}
