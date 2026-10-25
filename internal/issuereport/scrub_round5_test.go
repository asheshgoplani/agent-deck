package issuereport

import (
	"fmt"
	"strings"
	"testing"
)

// Fake vendor-shaped tokens, assembled at run time so no literal token
// sits in the source.
func fakeTok(prefix, alphabet string, n int) string {
	var b strings.Builder
	b.WriteString(prefix)
	for i := 0; i < n; i++ {
		b.WriteByte(alphabet[(i*7+3)%len(alphabet)])
	}
	return b.String()
}

const (
	alnum = "aB3cD4eF5gH6iJ7kL8mN9oP0qR1sT2uVwXyZ"
	hexd  = "0123456789abcdef"
	b64ul = "aB3cD4eF5gH6iJ7kL8mN9oP0qR1sT2uVwXyZ_-"
)

// MINOR-R4-1: bare vendor tokens with no keyword next to them are
// replaced whole by <token>, and so is everything the github sanitizer
// script treats as a token.
func TestScrub_BareVendorTokens(t *testing.T) {
	withConfiguredIdentity(t, "", "")
	toks := map[string]string{
		"gitlab pat":        fakeTok("glpat-", b64ul, 20),
		"gitlab pat long":   fakeTok("glpat-", b64ul, 26),
		"gitlab deploy":     fakeTok("gldt-", b64ul, 24),
		"gitlab runner":     fakeTok("glrt-", b64ul, 24),
		"gitlab trigger":    fakeTok("glptt-", hexd, 40),
		"hugging face":      fakeTok("hf_", alnum, 34),
		"hugging face min":  fakeTok("hf_", alnum, 30),
		"stripe sk live":    fakeTok("sk_live_", alnum, 24),
		"stripe sk test":    fakeTok("sk_test_", alnum, 24),
		"stripe rk live":    fakeTok("rk_live_", alnum, 24),
		"stripe rk test":    fakeTok("rk_test_", alnum, 99),
		"stripe pk live":    fakeTok("pk_live_", alnum, 24),
		"underscore sk":     fakeTok("sk_", alnum, 20),
		"shopify pat":       fakeTok("shpat_", hexd, 32),
		"shopify secret":    fakeTok("shpss_", hexd, 32),
		"shopify custom":    fakeTok("shpca_", hexd, 32),
		"shopify private":   fakeTok("shppa_", hexd, 32),
		"sendgrid":          fakeTok("SG.", b64ul, 22) + fakeTok(".", b64ul, 43),
		"github oauth":      fakeTok("gho_", alnum, 36),
		"slack xoxs":        fakeTok("xoxs-", alnum, 20),
		"postman":           fakeTok("PMAK-", alnum, 24) + fakeTok("-", hexd, 34),
		"google":            fakeTok("AIza", b64ul, 35),
		"telegram bot":      "123456789:" + fakeTok("", b64ul, 35),
		"telegram bot dash": "1234567890:" + fakeTok("", "aB3-", 35),
	}
	for name, tok := range toks {
		for _, ctx := range []string{"%s", "used %s yesterday", "(%s)", "\"%s\"", "url ?k=%s&x=1", "a %s, b"} {
			in := fmt.Sprintf(ctx, tok)
			want := fmt.Sprintf(ctx, PlaceholderToken)
			t.Run(name+"/"+ctx, func(t *testing.T) {
				got := Scrub(in, testHome)
				if got != want {
					t.Fatalf("Scrub(%q)\n got %q\nwant %q", in, got, want)
				}
				if again := Scrub(got, testHome); again != got {
					t.Fatalf("not idempotent: %q -> %q", got, again)
				}
			})
		}
		checkRoundTrip(t, name, Report{Kind: KindBug, Title: "push failed", WhatHappened: "the key " + tok + " leaked"},
			Diagnostics{Version: "1.16.30", LogExcerpt: "auth with " + tok + " failed"})
	}
	// Telegram chat ids, as the github sanitizer flags them.
	for _, c := range [][2]string{
		{"chat_id=", "123456789"},
		{"chat-id: ", "-1001234567890"},
		{`"chat_id":`, "987654321"},
		{"chatid ", "1234567"},
	} {
		in, want := c[0]+c[1], c[0]+PlaceholderToken
		if got := Scrub(in, testHome); got != want {
			t.Errorf("Scrub(%q) = %q, want %q", in, got, want)
		}
	}
	// Words that only look like the prefixes stay.
	for _, keep := range []string{
		"disk_usage_percentage_value", "the sk_buff struct", "hf_ratio", "glpat-short", "SG.short.x",
		"shpat_notahexvalue", "task_live_test_xyz", "chat_id unknown", "pk_fields",
	} {
		if got := Scrub(keep, testHome); got != keep {
			t.Errorf("Scrub(%q) = %q, want it kept", keep, got)
		}
	}
}
