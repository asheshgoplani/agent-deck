package issuereport

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

// The ssh config of the round-3 review: shared logins (git), personal
// logins (asheshg, macbuilder), a common alias, distinctive aliases and
// two plain-word aliases of one HostName.
const review3SSHConfig = `
Host github.com
    User git
Host dev
    HostName 10.20.30.40
    User asheshg
Host buildmac-direct
    HostName 100.64.0.9
    User macbuilder
Host backup office
    HostName nas.home.arpa
Host deckbox
Host *.corp
    User someone
`

const review3Remotes = `
[remotes.zebrafarm]
host = "farmer@zebrafarm:2222"
[remotes.mac]
host = "maxu@mac-studio.local"
`

// MAJOR-N11: "-l <login>" is an ssh login only right after ssh and its
// options, on the same line, with a space before the login. ls -la,
// bash -lc, wc -l and tmux send-keys -l stay as typed.
func TestScrub_SSHLoginNeedsARealDashL(t *testing.T) {
	withConfiguredIdentity(t, "", commonSSHConfig)
	checkScrub(t, []struct{ name, in, want string }{
		{"ls -la after ssh line", "1. ssh into the remote\n2. ls -la shows nothing", "1. ssh into the remote\n2. ls -la shows nothing"},
		{"ls -la alone", "ls -la shows nothing in the worktree", "ls -la shows nothing in the worktree"},
		{"wc -l after ssh line", "1. ssh dev\nwc -l notes.txt prints 0", "1. ssh <host>\nwc -l notes.txt prints 0"},
		{"wc -l same line", "ssh dev then wc -l notes.txt", "ssh <host> then wc -l notes.txt"},
		{"send-keys -l", "my ssh remote session froze\nargs=\"send-keys -t agentdeck_x_1 -l hello\"", "my ssh remote session froze\nargs=\"send-keys -t agentdeck_x_1 -l hello\""},
		{"one field", "ssh dev; ls -la; bash -lc make", "ssh <host>; ls -la; bash -lc make"},
		{"bash -lc same line", "ssh dev bash -lc make", "ssh <host> bash -lc make"},
		{"ssh at line end", "1. run ssh\n2. make build", "1. run ssh\n2. make build"},
		{"ssh -p at line end", "ssh -p 22\nbuildbox", "ssh -p 22\nbuildbox"},
		// real logins still go
		{"ssh -l login host", "ssh -l maxu farbox", "ssh -l <user> <host>"},
		{"ssh opts -l login", "ssh -p 2222 -l maxu farbox", "ssh -p 2222 -l <user> <host>"},
		{"ssh host -l login", "ssh farbox -l maxu", "ssh <host> -l <user>"},
		{"mosh two spaces", "mosh -l  maxu farbox", "mosh -l  <user> <host>"},
	})
}

// MAJOR-N12: distinctive configured names are scrubbed next to a dot;
// common and short names keep the dot guard (pkg.go.dev, .git).
func TestScrub_DistinctiveNamesNextToADot(t *testing.T) {
	withConfiguredIdentity(t, review3Remotes, review3SSHConfig)
	checkScrub(t, []struct{ name, in, want string }{
		{"name.sock", "socket buildmac-direct.sock missing", "socket <host>.sock missing"},
		{"x.name", "opened agentdeck.buildmac-direct session", "opened agentdeck.<host> session"},
		{"remote.cfg", "zebrafarm.cfg loaded", "<host>.cfg loaded"},
		{"alias.txt", "open deckbox.example.txt and x.deckbox", "open <host>.example.txt and x.<host>"},
		{"pkg.go.dev", "see https://pkg.go.dev/net/http", "see https://pkg.go.dev/net/http"},
		{"dot git", "fatal: not a git repository: .git", "fatal: not a git repository: .git"},
		{"golang.org", "import golang.org/x/net", "import golang.org/x/net"},
		{"dev.to", "read dev.to and main.go", "read dev.to and main.go"},
	})
}

// MINOR-N13: ssh-config User values are scrubbed as plain words unless
// they are common shared logins.
func TestScrub_SSHConfigUsersAreWords(t *testing.T) {
	withConfiguredIdentity(t, review3Remotes, review3SSHConfig)
	checkScrub(t, []struct{ name, in, want string }{
		{"two users", "asheshg ran it; macbuilder ran it", "<user> ran it; <user> ran it"},
		{"login as", "login as asheshg on the box", "login as <user> on the box"},
		{"shared logins stay", "git push as root", "git push as root"},
		{"wildcard block user", "someone else", "<user> else"},
	})
}

// NIT-N14: a letters-only ssh alias that only points at another HostName
// (Host backup office / HostName nas...) is replaced in host slots, not
// in prose. An alias that is the host's real name (no HostName), agent-deck
// remote names and names with a digit, '-' or '.' stay replaced everywhere.
func TestScrub_PlainAliasesOnlyInHostSlots(t *testing.T) {
	withConfiguredIdentity(t, review3Remotes, review3SSHConfig)
	checkScrub(t, []struct{ name, in, want string }{
		{"prose", "backup job on office failed", "backup job on office failed"},
		{"ssh slot", "ssh office uptime", "ssh <host> uptime"},
		{"scp slot", "scp f backup:/tmp", "scp f <host>:/tmp"},
		{"tmux slot", `"agentdeck_office_12"`, `"agentdeck_<host>_12"`},
		{"host port slot", "connect to host office port 22", "connect to host <host> port 22"},
		{"real name alias", "froze on deckbox today", "froze on <host> today"},
		{"remote name", "zebrafarm sync failed for farmer", "<host> sync failed for <user>"},
		{"distinctive alias", "On buildmac-direct in ~/x", "On <host> in ~/x"},
	})
}

// fuzzAtoms are the pieces the property test glues together: the review's
// repros, shell snippets, log lines, hosts, logins, tokens and markdown.
var fuzzAtoms = []string{
	"ssh dev", "ssh into the remote", "ls -la", "wc -l notes.txt", "bash -lc make", "send-keys -t agentdeck_x_1 -l hello",
	"ssh -l root", "ssh -p 2222 -l maxu farbox", "mosh dev", "dev", "git", "root", "buildmac-direct", "zebrafarm",
	"farmer", "office", "backup", "asheshg", "macbuilder", "deckbox", "me@example.com", "10.1.2.3", "fd7a::1",
	"host: dev", "server=zebrafarm", "remote: x", "remote add zebrafarm", "Remote \"office\"", "lookup zebrafarm on 1.1.1.1:53",
	"connect to host deckbox port 22", "to buildmac-direct:22", "https://farbox:8443/ui", "agentdeck_buildmac-direct_12",
	"buildmac-direct.sock", "zebrafarm.cfg", "agentdeck.buildmac-direct", "pkg.go.dev", ".git", "golang.org/x/net",
	"/home/carol/x", "/home/alicew/src", "~/src", "/Users/bob/y", "3f2a9c1e8b7d6a5f4e3d2c1b0a9f8e7d6c5b4a39",
	"curl -u a:b", "--password  pw1", "--token abc", "token=abc123def", "Authorization: Bearer abcdefgh12345",
	"https://hooks.slack.com/services/T1/B2/abcdef", "mysql -u r -pXy1", "git@dev:x", "scp f office:/tmp",
	"aa:bb:cc:dd:ee:ff", "-----BEGIN RSA " + "PRIVATE KEY-----", "MIIEpAIBAAKCAQEA" + strings.Repeat("b", 40),
	"[bug]", "### Logs (redacted)", "# heading", "--- rule", "`", "```", "<host>", "<token>", "<user>",
	strings.Repeat("x", 45), strings.Repeat("Ab1", 20), "host:", "@", "user@", "https://user:pw@example.com/x",
	"v1.16.30", "TestFooBarBazQuxQuuxCorgeGraultGarplyWaldoFred", "Actual:", "Expected:", "\t", "é漢字🙂",
	"time=2026-10-23T07:00:00Z level=ERROR msg=\"ssh failed\"", `{"host":"deckbox","msg":"x"}`, "devbox7",
	"git push failed: run git status in the dev worktree as root", "claude", "mac", "agent-deck", "tmux 3.4",
}

var fuzzSeps = []string{" ", "", ":", "/", ".", "-", "_", "@", "=", ", ", "\n", "; ", " | ", "\n\n"}

func fuzzText(r *rand.Rand, n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteString(fuzzAtoms[r.Intn(len(fuzzAtoms))])
		b.WriteString(fuzzSeps[r.Intn(len(fuzzSeps))])
	}
	return b.String()
}

// The review's four repros (MAJOR-N11) as reports.
var review3Repros = []struct {
	r   Report
	log string
}{
	{Report{Kind: KindBug, Title: "worktree empty", WhatHappened: "ls -la shows nothing in the worktree", Steps: "1. ssh into the remote"}, ""},
	{Report{Kind: KindBug, Title: "count wrong", WhatHappened: "wc -l notes.txt prints 0", Steps: "1. ssh dev"}, ""},
	{Report{Kind: KindBug, WhatHappened: "my ssh remote session froze"}, `time=x level=INFO msg=send args="send-keys -t agentdeck_x_1 -l hello"`},
	{Report{Kind: KindBug, WhatHappened: "ssh dev; ls -la; bash -lc make"}, ""},
}

// MAJOR-N11: Scrub is idempotent and a Build preview survives
// SanitizeIssue byte-identical, over a deterministic corpus of 600
// generated reports (seeds 1-300 with one config, 301-600 with another)
// plus the review's repros.
func TestScrub_IdempotentAndPreviewRoundTrips(t *testing.T) {
	for _, cfg := range []struct {
		name, toml, ssh string
		seeds           [2]int64
	}{
		{"review3", review3Remotes, review3SSHConfig, [2]int64{1, 300}},
		{"common", "", commonSSHConfig, [2]int64{301, 600}},
	} {
		t.Run(cfg.name, func(t *testing.T) {
			withConfiguredIdentity(t, cfg.toml, cfg.ssh)
			for i, rp := range review3Repros {
				checkRoundTrip(t, fmt.Sprintf("repro%d", i+1), rp.r, Diagnostics{Version: "1.16.30", OS: "linux", LogExcerpt: rp.log})
			}
			fails := 0
			for seed := cfg.seeds[0]; seed <= cfg.seeds[1] && fails < 5; seed++ {
				r := rand.New(rand.NewSource(seed))
				var log []string
				for n := r.Intn(30); n >= 0; n-- {
					log = append(log, fuzzText(r, 1+r.Intn(12)))
				}
				kind := KindBug
				if seed%7 == 0 {
					kind = KindIdea
				}
				rep := Report{Kind: kind, Title: fuzzText(r, r.Intn(6)), WhatHappened: fuzzText(r, 1+r.Intn(15)),
					Expected: fuzzText(r, r.Intn(8)), Steps: fuzzText(r, r.Intn(8)), Tool: fuzzText(r, r.Intn(2))}
				d := Diagnostics{Version: "1.16.30", OS: "Linux (Ubuntu 24.04.5 LTS)", Arch: "amd64",
					Terminal: "xterm-256color, tmux 3.4", LogExcerpt: strings.Join(log, "\n")}
				for _, s := range []string{rep.Title, rep.WhatHappened, d.LogExcerpt} {
					once := Scrub(s, testHome)
					if twice := Scrub(once, testHome); twice != once {
						fails++
						t.Errorf("seed %d: Scrub not idempotent\nin:    %q\nonce:  %q\ntwice: %q", seed, s, once, twice)
					}
				}
				if !checkRoundTrip(t, fmt.Sprintf("seed %d", seed), rep, d) {
					fails++
				}
			}
		})
	}
}

func checkRoundTrip(t *testing.T, name string, r Report, d Diagnostics) bool {
	t.Helper()
	is := Build(r, d)
	out, err := SanitizeIssue(r.Kind, is)
	if err != nil {
		t.Errorf("%s: SanitizeIssue refused a Build preview: %v\n%s", name, err, is.Body)
		return false
	}
	if out.Title != is.Title || out.Body != is.Body {
		t.Errorf("%s: preview changed on rescrub\ntitle %q -> %q\nbody before: %q\nbody after:  %q", name, is.Title, out.Title, is.Body, out.Body)
		return false
	}
	return true
}

// The previews of the review's repros keep the shell text as typed.
func TestBuild_ReviewReprosKeepShellText(t *testing.T) {
	withConfiguredIdentity(t, review3Remotes, review3SSHConfig)
	for i, want := range []string{
		"Actual: ls -la shows nothing in the worktree",
		"Actual: wc -l notes.txt prints 0",
		`send-keys -t agentdeck_x_1 -l hello`,
		"Actual: ssh <host>; ls -la; bash -lc make",
	} {
		rp := review3Repros[i]
		is := Build(rp.r, Diagnostics{LogExcerpt: rp.log})
		if !strings.Contains(is.Body, want) {
			t.Errorf("repro %d: body lacks %q\n%s", i+1, want, is.Body)
		}
	}
}

// The form's heading lines are fixed text: a configured name that is also
// a heading word ("terminal", "redacted") does not break the sections, so
// the preview still round-trips through SanitizeIssue.
func TestBuild_HeadingWordsAsHostNames(t *testing.T) {
	withConfiguredIdentity(t, `
[remotes.terminal]
host = "redacted"
[remotes.reproduce]
host = "local"
`, "")
	r := Report{Kind: KindBug, WhatHappened: "the terminal froze", Steps: "reproduce: open it"}
	is := Build(r, Diagnostics{Version: "1.2.3", OS: "linux", Terminal: "xterm", LogExcerpt: "redacted box down"})
	for _, h := range Headings(KindBug) {
		if !strings.Contains(is.Body, "### "+h+"\n") {
			t.Fatalf("heading %q lost\n%s", h, is.Body)
		}
	}
	if !strings.Contains(is.Body, "Actual: the <host> froze") || !strings.Contains(is.Body, "<host> box down") {
		t.Fatalf("configured names not scrubbed in the text\n%s", is.Body)
	}
	if !checkRoundTrip(t, "heading words", r, Diagnostics{Version: "1.2.3", OS: "linux", Terminal: "xterm", LogExcerpt: "redacted box down"}) {
		t.FailNow()
	}
}
