package issuereport

import (
	"strings"
	"testing"
)

// Fake secrets are assembled at run time so no token-shaped literal sits
// in the source (secret scanners, the GitHub sanitize gate).
func fake(prefix string, n int) string { return prefix + strings.Repeat("aB3", n)[:n] }

const testHome = "/home/alicew"

func TestScrub_Table(t *testing.T) {
	ghp := fake("gh"+"p_", 36)
	gho := fake("gh"+"o_", 36)
	pat := fake("github"+"_pat_", 40)
	sk := fake("s"+"k-", 40)
	skAnt := "s" + "k-ant-api03-" + fake("", 40)
	xoxb := "xo" + "xb-1234567890-" + fake("", 20)
	akia := "AK" + "IA" + "ABCDEFGHIJKLMNOP"
	jwt := "ey" + "J" + fake("", 20) + "." + fake("", 30) + "." + fake("", 20)
	bot := "123456789:" + fake("", 35)

	cases := []struct {
		name, in, want string
	}{
		{"home path", "open " + testHome + "/proj/agent-deck/main.go failed", "open ~/proj/agent-deck/main.go failed"},
		{"bare home", "HOME is " + testHome, "HOME is ~"},
		{"sibling home kept apart", "path /home/alicew2/x", "path ~/x"},
		{"other user home", "read /Users/bob/Library/x.plist", "read ~/Library/x.plist"},
		{"gh classic token", "token " + ghp + " here", "token <token> here"},
		{"gh oauth token", "auth=" + gho, "auth=<token>"},
		{"github pat", "use " + pat, "use <token>"},
		{"openai sk", "key " + sk, "key <token>"},
		{"anthropic sk-ant", "ANTHROPIC " + skAnt + " end", "ANTHROPIC <token> end"},
		{"slack xox", "slack " + xoxb, "slack <token>"},
		{"aws akia", "aws " + akia + " id", "aws <token> id"},
		{"jwt", "jwt " + jwt, "jwt <token>"},
		{"telegram bot token", "bot " + bot, "bot <token>"},
		{"bearer header", "Authorization: Bearer abcdefgh12345678", "Authorization: Bearer <token>"},
		{"key=value secret", "OPENAI_API_KEY=hunter2hunter2 next", "OPENAI_API_KEY=<token> next"},
		{"password colon", `password: "swordfish"`, `password: <token>`},
		{"url credentials", "git clone https://bob:s3cret@git.example.com/r.git", "git clone https://<token>@<host>/r.git"},
		{"private key", "-----BEGIN OPENSSH PRIVATE KEY-----\nabc\n-----END OPENSSH PRIVATE KEY-----", "<private-key>"},
		{"email", "mail me at jane.doe+x@example.co.uk now", "mail me at <email> now"},
		{"user at host", "ssh alicew@devbox failed", "ssh <user>@<host> failed"},
		{"ssh target bare", "ssh -p 2222 -A buildmac-direct uptime", "ssh -p 2222 -A <host> uptime"},
		{"host assignment", `remote host=mybox port=22`, `remote host=<host> port=22`},
		{"fqdn", "connect to build01.office.internal timed out", "connect to <host> timed out"},
		{"public host kept", "see https://github.com/asheshgoplani/agent-deck/issues/1", "see https://github.com/asheshgoplani/agent-deck/issues/1"},
		{"ipv4", "dial 192.168.1.20:22 refused", "dial <ip>:22 refused"},
		{"ipv6", "addr fe80::1ff:fe23:4567:890a%eth0 up", "addr <ip> up"},
		{"ipv6 full", "peer 2001:0db8:85a3:0000:0000:8a2e:0370:7334", "peer <ip>"},
		{"ipv6 bracket", "[2001:db8::1]:443", "[<ip>]:443"},
		{"local hostname bare", "running on devbox7 since boot", "running on <host> since boot"},
		{"local fqdn", "devbox7.corp.example.lan says hi", "<host> says hi"},
		{"login name bare", "user alicew started", "user <user> started"},
		// Things that must survive.
		{"file names", "edit main.go and debug.log and config.toml", "edit main.go and debug.log and config.toml"},
		{"version pin", "go install pkg@v1.2.3 and npm i x@latest", "go install pkg@v1.2.3 and npm i x@latest"},
		{"timestamp", "at 10:30:00 the pane froze", "at 10:30:00 the pane froze"},
		{"plain prose", "Typed letters doubled in a deckbox session", "Typed letters doubled in a deckbox session"},
		{"version", "agent-deck v1.16.33", "agent-deck v1.16.33"},
		{"mac app bundle", "iTerm.app 3.5 and Terminal.app", "iTerm.app 3.5 and Terminal.app"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Scrub(tc.in, testHome)
			if got != tc.want {
				t.Fatalf("Scrub(%q)\n got: %q\nwant: %q", tc.in, got, tc.want)
			}
			if again := Scrub(got, testHome); again != got {
				t.Fatalf("Scrub not idempotent: %q -> %q", got, again)
			}
		})
	}
}

// Every private value in a realistic log blob is gone afterwards.
func TestScrub_NothingLeft(t *testing.T) {
	secrets := []string{
		fake("gh"+"p_", 36), fake("gh"+"s_", 36), fake("github"+"_pat_", 40), fake("s"+"k-", 40),
		"xo" + "xp-99-" + fake("", 24), "AK" + "IA" + "QWERTYUIOPASDFGH",
		"ey" + "J" + fake("", 20) + "." + fake("", 30) + "." + fake("", 20),
		"hunter2hunter2", "jane@example.com", "10.0.0.7", "fe80::abcd:1", "2001:db8::42",
		"build01.office.internal", "devbox7", "alicew", testHome, "/Users/bob",
		"s3cretpw", "privhost",
	}
	blob := strings.Join([]string{
		`{"time":"2026-10-22T23:40:00Z","level":"INFO","msg":"spawn","path":"` + testHome + `/work/x","host":"privhost"}`,
		"export GITHUB_TOKEN=" + secrets[0],
		"GH_TOKEN: " + secrets[1] + " and " + secrets[2],
		"curl -H 'Authorization: Bearer " + secrets[3] + "' https://api.example.io/v1",
		"slack " + secrets[4] + " aws " + secrets[5] + " jwt " + secrets[6],
		"db_password=" + secrets[7] + " owner " + secrets[8],
		"dial tcp " + secrets[9] + ":443 and [" + secrets[10] + "]:22 and " + secrets[11],
		"ssh -i ~/.ssh/id build01.office.internal; on devbox7 as alicew",
		"cat /Users/bob/notes.txt",
		"https://bob:s3cretpw@git.example.com/x.git",
	}, "\n")
	got := Scrub(blob, testHome)
	for _, s := range secrets {
		if strings.Contains(got, s) {
			t.Errorf("Scrub left %q in output:\n%s", s, got)
		}
	}
	if strings.Contains(got, "/home/") || strings.Contains(got, "/Users/") {
		t.Errorf("home path left:\n%s", got)
	}
}
