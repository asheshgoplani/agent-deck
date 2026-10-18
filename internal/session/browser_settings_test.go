package session

import (
	"strings"
	"testing"
)

func TestResolveBrowserEnabled(t *testing.T) {
	on, off := true, false
	for _, c := range []struct {
		name string
		cfg  *UserConfig
		want bool
	}{
		{"nil", nil, false},
		{"default off", &UserConfig{}, false},
		{"global on", &UserConfig{Browser: BrowserSettings{Enabled: &on}}, true},
		{"profile on", &UserConfig{Profiles: map[string]ProfileSettings{"a": {Browser: &BrowserSettings{Enabled: &on}}}}, true},
		{"profile off over global on", &UserConfig{Browser: BrowserSettings{Enabled: &on}, Profiles: map[string]ProfileSettings{"a": {Browser: &BrowserSettings{Enabled: &off}}}}, false},
		{"other profile on", &UserConfig{Profiles: map[string]ProfileSettings{"b": {Browser: &BrowserSettings{Enabled: &on}}}}, false},
	} {
		if got := ResolveBrowserEnabled(c.cfg, "a"); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
	msg := BrowserDisabledMessage("a")
	for _, want := range []string{"agent-deck config set browser.enabled true", "agent-deck config set profiles.a.browser.enabled true", "[browser] enabled = true"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message lacks %q: %s", want, msg)
		}
	}
	if k, ok := LookupConfigKey("browser.enabled"); !ok || k.Get(&UserConfig{}) != false {
		t.Fatal("browser.enabled key: default must be off")
	}
}
