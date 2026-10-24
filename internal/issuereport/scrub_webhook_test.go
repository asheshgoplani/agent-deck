package issuereport

import (
	"strings"
	"testing"
)

// CodeQL go/regex/missing-regexp-anchor on the webhook pattern: the host
// is now bounded on both sides (start of text or a non-host character on
// the left, an optional :port and the path on the right). The first group
// slipped through the old pattern and leaked the webhook secret; the
// second group was caught before and must stay caught.
func TestScrub_WebhookHostBoundaries(t *testing.T) {
	sec := fake("", 24)
	cases := []struct{ name, in, want string }{
		// slipped through before
		{"slack with port", "post https://hooks.slack.com:443/services/T0ABCDEF/B0ABCDEF/" + sec + " ok",
			"post https://<host>:443/services/<token> ok"},
		{"discord with port", "post https://discord.com:443/api/webhooks/123456789012345678/" + sec,
			"post https://<host>:443/api/webhooks/<token>"},
		{"discord versioned api", "post https://discord.com/api/v10/webhooks/123456789012345678/" + sec,
			"post https://<host>/api/v10/webhooks/<token>"},
		{"govslack", "post https://hooks.slack-gov.com/services/T0ABCDEF/B0ABCDEF/" + sec,
			"post https://<host>/services/<token>"},
		// caught before, still caught
		{"discord subdomain", "post https://canary.discord.com/api/webhooks/123456789012345678/" + sec,
			"post https://<host>/api/webhooks/<token>"},
		{"discordapp", "post https://discordapp.com/api/webhooks/123456789012345678/" + sec,
			"post https://<host>/api/webhooks/<token>"},
		{"dash before host", "post https://x-hooks.slack.com/services/T0ABCDEF/B0ABCDEF/" + sec,
			"post https://<host>/services/<token>"},
		{"upper case", "POST HTTPS://HOOKS.SLACK.COM/SERVICES/T0ABCDEF/B0ABCDEF/" + sec,
			"POST HTTPS://<host>/SERVICES/<token>"},
		{"start of text", "hooks.slack.com/workflows/T0ABCDEF/" + sec,
			"<host>/workflows/<token>"},
		{"quoted", `url="https://hooks.slack.com/triggers/T0ABCDEF/` + sec + `"`,
			`url="https://<host>/triggers/<token>"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Scrub(tc.in, testHome)
			if strings.Contains(got, sec) {
				t.Fatalf("webhook secret leaked:\n in:  %q\n got: %q", tc.in, got)
			}
			if got != tc.want {
				t.Errorf("Scrub(%q)\n got:  %q\n want: %q", tc.in, got, tc.want)
			}
		})
	}
}
