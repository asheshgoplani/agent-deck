// site_pageviews_test.go pins the PostHog pageview snippet on the website
// under site/ (deployed by pages.yml), ASK-119. Before it the site sent no
// $pageview at all. The snippet must stay privacy-preserving:
//
//  1. Every HTML page carries the official loader and posthog.init with the
//     public phc_ project key, the EU api_host, in-memory persistence (no
//     cookies, no localStorage) and pageviews only.
//  2. Do-Not-Track and Global Privacy Control stop the snippet before the
//     loader runs, and respect_dnt is set as a second line of defence.
//  3. A Content-Security-Policy meta, if one is ever added, must allow the
//     PostHog EU hosts.
//  4. TELEMETRY.md documents the website collection.
package releasetests

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// sitePage is one HTML document under site/.
type sitePage struct {
	name string
	body string
}

// siteHTMLPages returns every HTML document under site/. Search-console
// verification files (a bare "google-site-verification: ..." line served as
// .html) are skipped: their content must stay byte-exact for verification.
func siteHTMLPages(t *testing.T) []sitePage {
	t.Helper()
	root := filepath.Join(repoRoot(t), "site")
	var pages []sitePage
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || filepath.Ext(path) != ".html" {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		body := string(raw)
		if strings.HasPrefix(strings.TrimSpace(body), "google-site-verification:") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		pages = append(pages, sitePage{name: "site/" + filepath.ToSlash(rel), body: body})
		return nil
	})
	if err != nil {
		t.Fatalf("walk site/: %v", err)
	}
	if len(pages) == 0 {
		t.Fatal("no HTML pages found under site/")
	}
	return pages
}

// posthogBlock returns the <script> element holding posthog.init, or "".
func posthogBlock(body string) string {
	for _, m := range regexp.MustCompile(`(?s)<script>(.*?)</script>`).FindAllStringSubmatch(body, -1) {
		if strings.Contains(m[1], "posthog.init(") {
			return m[1]
		}
	}
	return ""
}

var posthogKeyRe = regexp.MustCompile(`posthog\.init\('([^']*)'`)

// TestSite_PostHogSnippetOnEveryPage asserts each page loads the official
// PostHog loader with the EU host and the cookieless, pageview-only config.
func TestSite_PostHogSnippetOnEveryPage(t *testing.T) {
	for _, p := range siteHTMLPages(t) {
		t.Run(p.name, func(t *testing.T) {
			block := posthogBlock(p.body)
			if block == "" {
				t.Fatalf("%s has no PostHog snippet (no <script> with posthog.init)", p.name)
			}
			if headEnd := strings.Index(p.body, "</head>"); headEnd < 0 || strings.Index(p.body, block) > headEnd {
				t.Errorf("%s: PostHog snippet must sit in <head>", p.name)
			}
			for _, want := range []string{
				// Official loader: async script from the EU assets host,
				// derived from api_host.
				`s.api_host.replace(".i.posthog.com","-assets.i.posthog.com")+"/static/array.js"`,
				`p.async=!0`,
				`api_host: 'https://eu.i.posthog.com'`,
				`persistence: 'memory'`,
				`respect_dnt: true`,
				`capture_pageview: true`,
				`autocapture: false`,
				`disable_session_recording: true`,
				`disable_surveys: true`,
				`person_profiles: 'identified_only'`,
				// Project settings are shared with the app and can switch on
				// more capture remotely; pin everything beyond pageviews off.
				`advanced_disable_flags: true`,
				`advanced_disable_decide: true`,
				`capture_heatmaps: false`,
				`capture_dead_clicks: false`,
				`capture_performance: false`,
				`capture_exceptions: false`,
				`disable_web_experiments: true`,
			} {
				if !strings.Contains(block, want) {
					t.Errorf("%s: PostHog snippet lacks %s", p.name, want)
				}
			}
			for _, banned := range []string{"us.i.posthog.com", "persistence: 'cookie'", "persistence: 'localStorage", "autocapture: true"} {
				if strings.Contains(block, banned) {
					t.Errorf("%s: PostHog snippet contains %s", p.name, banned)
				}
			}
		})
	}
}

// TestSite_PostHogRespectsDoNotTrack asserts the DNT/GPC guard returns
// before the loader is defined or posthog.init is called, so a browser
// signalling DNT never fetches array.js.
func TestSite_PostHogRespectsDoNotTrack(t *testing.T) {
	for _, p := range siteHTMLPages(t) {
		t.Run(p.name, func(t *testing.T) {
			block := posthogBlock(p.body)
			if block == "" {
				t.Fatalf("%s has no PostHog snippet", p.name)
			}
			for _, signal := range []string{
				`navigator.doNotTrack === '1'`,
				`window.doNotTrack === '1'`,
				`navigator.msDoNotTrack === '1'`,
				`navigator.globalPrivacyControl === true`,
			} {
				if !strings.Contains(block, signal) {
					t.Errorf("%s: DNT guard does not check %s", p.name, signal)
				}
			}
			guard := strings.Index(block, "if (dnt) return;")
			if guard < 0 {
				t.Fatalf("%s: no `if (dnt) return;` guard in the PostHog snippet", p.name)
			}
			loader := strings.Index(block, "!function(t,e)")
			initCall := strings.Index(block, "posthog.init(")
			if loader < 0 || guard > loader || guard > initCall {
				t.Errorf("%s: DNT guard (at %d) must come before the loader (%d) and posthog.init (%d)", p.name, guard, loader, initCall)
			}
		})
	}
}

// TestSite_PostHogProjectKey asserts every page uses the same public,
// write-only phc_ project key.
func TestSite_PostHogProjectKey(t *testing.T) {
	keyFormat := regexp.MustCompile(`^phc_[A-Za-z0-9_-]{32,80}$`)
	var first string
	for _, p := range siteHTMLPages(t) {
		m := posthogKeyRe.FindStringSubmatch(posthogBlock(p.body))
		if m == nil {
			t.Errorf("%s: no posthog.init('<key>', ...) call", p.name)
			continue
		}
		// Never print the key itself; report only its shape.
		if !keyFormat.MatchString(m[1]) {
			t.Errorf("%s: project key is not a phc_ public project key (len %d)", p.name, len(m[1]))
		}
		if first == "" {
			first = m[1]
		} else if m[1] != first {
			t.Errorf("%s: project key differs from the other site pages", p.name)
		}
	}
}

// TestSite_CSPAllowsPostHog asserts that a Content-Security-Policy meta, if a
// page has one, lets the snippet fetch array.js and send events.
func TestSite_CSPAllowsPostHog(t *testing.T) {
	cspRe := regexp.MustCompile(`(?is)<meta[^>]+http-equiv=["']Content-Security-Policy["'][^>]*content=["']([^"']*)["']`)
	for _, p := range siteHTMLPages(t) {
		m := cspRe.FindStringSubmatch(p.body)
		if m == nil {
			continue
		}
		for _, host := range []string{"https://eu-assets.i.posthog.com", "https://eu.i.posthog.com"} {
			if !strings.Contains(m[1], host) {
				t.Errorf("%s: Content-Security-Policy does not allow %s", p.name, host)
			}
		}
	}
}

// TestTelemetryDoc_DescribesWebsite asserts TELEMETRY.md has a Website
// section stating what the snippet does.
func TestTelemetryDoc_DescribesWebsite(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "TELEMETRY.md"))
	if err != nil {
		t.Fatalf("read TELEMETRY.md: %v", err)
	}
	doc := string(raw)
	start := strings.Index(doc, "\n## Website")
	if start < 0 {
		t.Fatal("TELEMETRY.md has no `## Website` section")
	}
	section := doc[start+1:]
	if end := strings.Index(section[len("## Website"):], "\n## "); end >= 0 {
		section = section[:len("## Website")+end]
	}
	for _, want := range []string{"$pageview", "Do Not Track", "Global Privacy Control", "memory", "cookies", "eu.i.posthog.com"} {
		if !strings.Contains(section, want) {
			t.Errorf("TELEMETRY.md Website section does not mention %q", want)
		}
	}
}
