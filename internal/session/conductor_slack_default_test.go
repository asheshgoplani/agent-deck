package session

import (
	"bytes"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// #2547/#2548: default_conductor has three meanings (absent = legacy first
// conductor, "" = no default, a name = that conductor). A config save must
// keep them apart, in particular it must not drop an explicit "" and so turn
// "no default" back into the legacy first-conductor routing.
func TestSlackSettingsDefaultConductor_TOMLRoundTrip(t *testing.T) {
	empty, named := "", "ops"
	for _, tc := range []struct {
		name     string
		in       *string
		wantLine string // "" means the key must be absent
	}{
		{"absent", nil, ""},
		{"explicit empty", &empty, `default_conductor = ""`},
		{"named", &named, `default_conductor = "ops"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			type wrap struct {
				Slack SlackSettings `toml:"slack"`
			}
			var buf bytes.Buffer
			if err := toml.NewEncoder(&buf).Encode(wrap{Slack: SlackSettings{BotToken: "xoxb", DefaultConductor: tc.in}}); err != nil {
				t.Fatalf("encode: %v", err)
			}
			out := buf.String()
			if tc.wantLine == "" && strings.Contains(out, "default_conductor") {
				t.Fatalf("absent key was written:\n%s", out)
			}
			if tc.wantLine != "" && !strings.Contains(out, tc.wantLine) {
				t.Fatalf("want %q in:\n%s", tc.wantLine, out)
			}

			var back wrap
			if _, err := toml.Decode(out, &back); err != nil {
				t.Fatalf("decode: %v", err)
			}
			got := back.Slack.DefaultConductor
			switch {
			case tc.in == nil && got != nil:
				t.Fatalf("absent decoded as %q", *got)
			case tc.in != nil && (got == nil || *got != *tc.in):
				t.Fatalf("decoded %v, want %q", got, *tc.in)
			}
		})
	}
}
