package issuereport

import (
	"strings"
	"testing"
)

// A common ssh config: shared logins (git, root) and short or common host
// aliases next to the distinctive ones.
const commonSSHConfig = `
Host github.com
    User git
Host dev
    HostName dev
    User root
Host claude mac
    User ubuntu
Host buildmac-direct
    HostName mac-mini-7
    User builder9
Host deckbox
Host dbx
`

func checkScrub(t *testing.T, cases []struct{ name, in, want string }) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Scrub(tc.in, testHome)
			if got != tc.want {
				t.Fatalf("Scrub(%q)\n got: %q\nwant: %q", tc.in, got, tc.want)
			}
			if again := Scrub(got, testHome); again != got {
				t.Fatalf("not idempotent: %q -> %q", got, again)
			}
		})
	}
}

// MINOR-N1: ssh-config User values are not identity words, and common or
// short host aliases are not replaced as bare words or inside dotted
// names; distinctive configured names still are, and every configured
// name is still replaced where only a host goes.
func TestScrub_CommonSSHConfigWordsStayReadable(t *testing.T) {
	withConfiguredIdentity(t, "", commonSSHConfig)
	checkScrub(t, []struct{ name, in, want string }{
		// readable
		{"git words", "git push failed: run git status in the dev worktree as root", "git push failed: run git status in the dev worktree as root"},
		{"dot git", "fatal: not a git repository (or any parent): .git", "fatal: not a git repository (or any parent): .git"},
		{"pkg.go.dev", "see https://pkg.go.dev/net/http", "see https://pkg.go.dev/net/http"},
		{"tool and app names", "claude crashed in the Mac app; mac build ok", "claude crashed in the Mac app; mac build ok"},
		// MINOR-N13 (round 4): a personal ssh-config login is a word to
		// scrub; a shared one (ubuntu) is not.
		{"ssh users", "ubuntu image, builder9 job", "ubuntu image, <user> job"},
		{"remote add origin", "git remote add origin https://github.com/a/b", "git remote add origin https://github.com/a/b"},
		{"remote show upstream", "git remote show upstream", "git remote show upstream"},
		{"server prose", "server: started on :8080", "server: started on :8080"},
		{"host false", "host: false", "host: false"},
		{"remote progress", "remote: Enumerating objects: 5", "remote: Enumerating objects: 5"},
		{"config path", "config:/tmp/x", "config:/tmp/x"},
		// MAJOR-N12 (round 4): a distinctive name is scrubbed next to a dot.
		{"inside dotted name", "open deckbox.example.txt and x.deckbox", "open <host>.example.txt and x.<host>"},
		{"short alias bare", "dbx is slow", "dbx is slow"},
		// still scrubbed
		{"distinctive bare", "On buildmac-direct in ~/x, deckbox and dbx too", "On <host> in ~/x, <host> and dbx too"},
		{"sentence end", "it froze on deckbox.", "it froze on <host>."},
		{"hostname value", "resolved mac-mini-7 ok", "resolved <host> ok"},
		{"ssh common alias", "ssh dev uptime", "ssh <host> uptime"},
		{"ssh -l login slot", "ssh -l builder9 dev", "ssh -l <user> <host>"},
		{"user at alias", "git@dev:repo.git", "<user>@<host>:repo.git"},
		{"host port slot", "connect to host dev port 22", "connect to host <host> port 22"},
		{"lookup slot", "lookup claude on 1.1.1.1:53", "lookup <host> on <ip>:53"},
		{"server configured", "server: dev", "server: <host>"},
		{"server hostish", "server: box-7 up", "server: <host> up"},
		{"tmux name", `"agentdeck_dev_12"`, `"agentdeck_<host>_12"`},
		{"remote add configured", "agent-deck remote add mac x", "agent-deck remote add <host> x"},
	})
}

// MINOR-N4: credentials in curl -u, --password X (space form) and webhook
// URL paths; look-alikes stay.
func TestScrub_FlagAndWebhookSecrets(t *testing.T) {
	slack := "https://hooks.slack.com/services/T0ABCDEF/B0ABCDEF/" + fake("", 24)
	discord := "https://discord.com/api/webhooks/123456789012345678/" + fake("", 40)
	checkScrub(t, []struct{ name, in, want string }{
		{"curl -u", "curl -u admin:hunter2 -s x", "curl -u <token> -s x"},
		{"curl --user", "curl -s --user admin:hunter2 x", "curl -s --user <token> x"},
		{"curl --user=", "curl --user=admin:hunter2 x", "curl --user=<token> x"},
		{"curl glued", "curl -uadmin:hunter2 x", "curl -u<token> x"},
		{"http-password", "wget --http-password hunter2 x", "wget --http-password <token> x"},
		{"password space", "tool --password hunter2 --x", "tool --password <token> --x"},
		{"token space", "cli --token abc123def --v", "cli --token <token> --v"},
		{"slack webhook", "post " + slack + " ok", "post https://<host>/services/<token> ok"},
		{"discord webhook", "post " + discord, "post https://<host>/api/webhooks/<token>"},
		// look-alikes stay
		{"docker uid", "docker run -u 1000:1000 img", "docker run -u 1000:1000 img"},
		{"curl -u no pass", "curl -u admin x", "curl -u admin x"},
		{"password-stdin", "docker login --password-stdin -u me", "docker login --password-stdin -u me"},
		{"token-file", "cli --token-file ~/t --v", "cli --token-file ~/t --v"},
		{"sort -u", "sort -u a:b", "sort -u a:b"},
	})
}

// NIT-N6: hex commit shas, sha256 digests and long CamelCase test names are
// identifiers and stay; real tokens of the same length are still scrubbed.
func TestScrub_DigestsAndTestNamesStayTokensDoNot(t *testing.T) {
	sha1 := "1af1da745d3c9c1f0f4f3a0e8b5f6a7c8d9e0f1a"
	sha256 := strings.Repeat("e3b0c44298fc1c14", 4)
	testName := "TimelineLoadNoticeTests.testAnEmptySessionsQuickAnswerLeavesNoLoadingNotice"
	goTest := "TestFeedbackReport_IssueFilesExactlyThePreviewEvenWhenTheLogChanged"
	checkScrub(t, []struct{ name, in, want string }{
		{"commit sha", "head " + sha1 + " ok", "head " + sha1 + " ok"},
		{"sha256 digest", "digest sha256:" + sha256, "digest sha256:" + sha256},
		{"bare digest", "sum " + sha256 + " file", "sum " + sha256 + " file"},
		{"swift test name", testName + " failed (0.69 s)", testName + " failed (0.69 s)"},
		{"go test name", "--- FAIL: " + goTest + " (0.00s)", "--- FAIL: " + goTest + " (0.00s)"},
		// real tokens stay scrubbed
		{"mixed 40", "key " + fake("", 40) + " end", "key <token> end"},
		{"mixed 48 base64", "blob " + fake("", 48) + "== end", "blob <token> end"},
		{"hex secret assigned", "api_key=" + sha1, "api_key=<token>"},
		{"gh token", "use " + fake("gh"+"p_", 36), "use <token>"},
		{"hex 41 is not a sha", "x " + sha1 + "b y", "x <token> y"},
	})
}
