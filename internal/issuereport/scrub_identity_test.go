package issuereport

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// withConfiguredIdentity points HOME and XDG_CONFIG_HOME at a temp dir that
// holds an agent-deck config.toml with [remotes] and an ~/.ssh/config, and
// makes Scrub read them through the real loaders.
func withConfiguredIdentity(t *testing.T, configTOML, sshConfig string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	session.ClearUserConfigCache()
	resetIdentity()
	t.Cleanup(func() { session.ClearUserConfigCache(); resetIdentity() })
	cfgPath, err := session.GetUserConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(cfgPath, home) {
		t.Fatalf("config path %q escapes the temp HOME %q", cfgPath, home)
	}
	for path, body := range map[string]string{cfgPath: configTOML, filepath.Join(home, ".ssh", "config"): sshConfig} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// MAJOR-1: names of agent-deck remotes and ssh-config hosts are scrubbed
// as bare words, even when they are not this machine's name.
func TestScrub_ConfiguredRemotesAndSSHHosts(t *testing.T) {
	withConfiguredIdentity(t, `
[remotes.deckbox]
host = "maxu@deckbox-wg:2222"
`, `
Host buildmac-direct
    HostName mac-mini-7
    User builder9
Host *.wild !nope gw?
    User x
Host=jumpbox
`)
	cases := []struct{ in, want string }{
		{"[bug] buildmac-direct: typing doubles letters", "[bug] <host>: typing doubles letters"},
		{"Actual: On buildmac-direct in ~/x it froze", "Actual: On <host> in ~/x it froze"},
		{`"target":"agentdeck_buildmac-direct_1234"`, `"target":"agentdeck_<host>_1234"`},
		{"agent-deck remote add deckbox now", "agent-deck remote add <host> now"},
		{"session on deckbox died", "session on <host> died"},
		{"via deckbox-wg port 2222", "via <host> port 2222"},
		{"resolved mac-mini-7 ok", "resolved <host> ok"},
		{"jumpbox is slow", "<host> is slow"},
		// ssh-config User values and the login of an agent-deck remote are
		// identity words (MINOR-N13); common shared logins are not.
		{"logged in as builder9 and maxu", "logged in as <user> and <user>"},
		{"ssh builder9@buildmac-direct", "ssh <user>@<host>"},
		// wildcard patterns are not names; nothing else is touched
		{"the wild nope gw path", "the wild nope gw path"},
	}
	for _, tc := range cases {
		if got := Scrub(tc.in, testHome); got != tc.want {
			t.Errorf("Scrub(%q)\n got: %q\nwant: %q", tc.in, got, tc.want)
		}
	}
}

// MAJOR-1: host names in places only a host goes are scrubbed even when
// the name is not configured anywhere (the Mac app does not know deckbox).
func TestScrub_HostSlots(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"host X port", `"error":"ssh: connect to host farbox port 22: Connection refused"`, `"error":"ssh: connect to host <host> port 22: Connection refused"`},
		{"lookup", "dial tcp: lookup farbox on 10.0.0.1:53: no such host", "dial tcp: lookup <host> on <ip>:53: no such host"},
		{"X:~", "rsync -a x (to farbox:~/src)", "rsync -a x (to <host>:~/src)"},
		{"X:/path", "scp file farbox:/tmp/x", "scp file <host>:/tmp/x"},
		{"scheme port", "open https://farbox:8443/ui now", "open https://<host>:8443/ui now"},
		{"scheme bare", "GET http://farbox/x", "GET http://<host>/x"},
		{"Remote quoted", `Remote "farbox" unreachable`, `Remote "<host>" unreachable`},
		{"remote colon", "remote farbox: tmux session missing", "remote <host>: tmux session missing"},
		{"remote add", "agent-deck remote add farbox user@x", "agent-deck remote add <host> <user>@<host>"},
		{"to X:port", "dial to farbox:22 failed", "dial to <host>:22 failed"},
		// readable things stay
		{"golang.org", "import golang.org/x/net/context", "import golang.org/x/net/context"},
		{"go.dev", "see https://pkg.go.dev/fmt", "see https://pkg.go.dev/fmt"},
		{"githubusercontent", "curl https://raw.githubusercontent.com/a/b", "curl https://raw.githubusercontent.com/a/b"},
		{"github", "https://github.com/asheshgoplani/agent-deck/issues/1", "https://github.com/asheshgoplani/agent-deck/issues/1"},
		{"localhost", "http://localhost:8080/x", "http://localhost:8080/x"},
		{"lookup prose", "session lookup failed", "session lookup failed"},
		{"failed to", "failed to connect: refused", "failed to connect: refused"},
		{"remote prose", "remote session missing", "remote session missing"},
		{"timestamp", "at 10:30:00 ok", "at 10:30:00 ok"},
	}
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

// MINOR-3, MINOR-4, NIT-9: secret shapes with '/' and '+', PEM fragments,
// mysql -p, user@ip, MAC addresses; normal paths stay readable.
func TestScrub_SecretShapesAndPaths(t *testing.T) {
	akia := "AK" + "IA" + "ABCDEFGHIJKLMNOP"
	awsSecret := "wJalrXUtnFEMI/K7MDENG/" + "bPxRfiCYEXAMPLEKEY"
	pemLine := "MIIEowIBAAKCAQEA3x9Z/8kLmN4b+Qx9Z/8kLmNabcdEFGHij" + "klMNOPqrstuvWXyz01"
	cases := []struct{ name, in, want string }{
		{"aws secret no key name", "aws " + akia + " " + awsSecret + " end", "aws <token> <token> end"},
		{"pem tail without BEGIN", pemLine + "\n" + "Qx9Z/8kLmN4b==\n-----END RSA PRIVATE KEY-----\nnext line", "<private-key>\nnext line"},
		{"pem fragment mid line", "key part a1B2c3D4e5F6g7H8i9J0/kLmNoPqRsTuVwXyZ+12 end", "key part <token> end"},
		{"certificate block", "-----BEGIN CERTIFICATE-----\n" + pemLine + "\n-----END CERTIFICATE-----", "<private-key>"},
		{"mysql -p", "mysql -u root -pS3cretPass mydb", "mysql -u root -p<token> mydb"},
		{"user at ip", "login maxu@100.64.0.5 ok", "login <user>@<ip> ok"},
		{"ssh user at ip", "ssh maxu@100.64.0.5", "ssh <user>@<ip>"},
		{"user at ipv6", "as maxu@[fd7a::5]:22", "as <user>@[<ip>]:22"},
		{"mac address", "ether 3c:22:fb:12:ab:cd up, hw 00-1A-2B-3C-4D-5E", "ether <mac> up, hw <mac>"},
		// stays readable
		{"home path", testHome + "/src/agent-deck/internal/ui/home.go:42", "~/src/agent-deck/internal/ui/home.go:42"},
		{"tilde path", "~/src/agent-deck/internal/ui/home.go", "~/src/agent-deck/internal/ui/home.go"},
		{"relative path", "Sources/AgentDeckMac/Views/ReportProblemSheet2.swift", "Sources/AgentDeckMac/Views/ReportProblemSheet2.swift"},
		{"short sha", "head 1af1da745d ok", "head 1af1da745d ok"},
		{"word next to a key stays", "<private-key>\nrestarted", "<private-key>\nrestarted"},
		{"mysql without pass", "mysql -u root -p mydb", "mysql -u root -p mydb"},
	}
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
