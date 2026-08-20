package agents

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The reviewer's twelve cases, six that must be refused and six that must be
// copied.
func TestScanForCredentialsReviewerCases(t *testing.T) {
	mustRefuse := []struct{ name, body string }{
		{"16-char app password, no spaces", "The Gmail app password is abcdefghijklmnop\n"},
		{"password on the line after its heading", "## Gmail app password\nabcd efgh ijkl mnop\n"},
		{"password inside a code fence", "## Gmail app password\n```\nabcd efgh ijkl mnop\n```\n"},
		{"dash-grouped key, no secret word", "Use 7f3a-91b2-cc40 when connecting to the bridge.\n"},
		{"32-char base64 blob", "Bridge value: aGVsbG9Xb3JsZFRoaXNJc0EzMkNoYXJz\n"},
		{"password inside a connection URI", "postgres://svc:hunter2brown@db.example.com:5432/app\n"},
	}
	for _, tc := range mustRefuse {
		if len(ScanForCredentials(tc.body)) == 0 {
			t.Errorf("MISS (should refuse) %s: %q", tc.name, tc.body)
		}
	}

	mustCopy := []struct{ name, body string }{
		{"policy prose with colon", "Never commit a token: use the connector store instead.\n"},
		{"policy prose heading", "Password handling: the agent never sees one.\n"},
		{"git SHA in learnings", "Fixed in 9f8e7d6c5b4a39281706f5e4d3c2b1a098765432 after review.\n"},
		{"kebab identifier", "See workflow release-candidate-verification-checklist-v2.\n"},
		{"secrets prose", "Secrets: never inline them in a role directory.\n"},
		{"api key rotation prose", "API key rotation: quarterly, tracked in the runbook.\n"},
	}
	for _, tc := range mustCopy {
		if lines := ScanForCredentials(tc.body); len(lines) != 0 {
			t.Errorf("FALSE POSITIVE (should copy) %s: %q -> lines %v", tc.name, tc.body, lines)
		}
	}
}

// His real POLICY.md line, and the one-character variant the reviewer said
// would break it.
func TestScanForCredentialsRealConductorProse(t *testing.T) {
	for _, line := range []string{
		`- "I need API keys / credentials / tokens"` + "\n",
		`- "I need API keys: ask the human"` + "\n",
		"Never merge without review.\nEscalate to the human on ambiguity.\n",
	} {
		if lines := ScanForCredentials(line); len(lines) != 0 {
			t.Errorf("FALSE POSITIVE on conductor prose: %q -> lines %v", line, lines)
		}
	}
}

// The regression test this needed from the start: the scanner must return
// nothing on the real conductor directory's own files.
//
// Round 3 refused the real 10,889-byte CLAUDE.md over "60s", "g14" and a
// [HEARTBEAT] example — dropping the entry point of the flagship adoption
// target — and nothing caught it, because every case pinned until then was a
// line I had written myself. Fixture copies of the real files live under
// testdata/ so this runs anywhere, with no dependency on the host's home.
func TestScanForCredentialsRealConductorFiles(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join("testdata", "conductor"))
	if err != nil {
		t.Skipf("conductor fixture not present: %v", err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		path := filepath.Join("testdata", "conductor", entry.Name())
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("read %s: %v", path, readErr)
		}
		if lines := ScanForCredentials(string(body)); len(lines) != 0 {
			bodyLines := strings.Split(string(body), "\n")
			for _, line := range lines {
				if line-1 < len(bodyLines) {
					t.Errorf("%s:%d flagged as a credential: %q", entry.Name(), line, bodyLines[line-1])
				}
			}
		}
	}
}

// The fleet's own vocabulary must survive the scanner.
func TestScanForCredentialsFleetVocabulary(t *testing.T) {
	for _, line := range []string{
		"Has built-in 60s wait for agent readiness.",
		"Advance the g14 fleet one bounded step per tick.",
		"Check the g14 fleet manager before restarting anything.",
		"Compare the sha256 digest before copying a role file.",
		"Write every report as utf8 text only.",
		"Build both x86 and arm targets before release.",
		"PR 1996 fixed the g14 fleet drain.",
		"## Secrets\nNever keep them near your code.",
		"Token handling rules follow.\nKeep them safe from harm when possible.",
	} {
		if lines := ScanForCredentials(line); len(lines) != 0 {
			t.Errorf("FALSE POSITIVE on fleet prose %q -> lines %v", line, lines)
		}
	}
}

// Shapes round 3 found were still missed.
func TestScanForCredentialsRoundThreeMisses(t *testing.T) {
	for name, body := range map[string]string{
		"base32 TOTP seed":        "Seed for the authenticator:\nJBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP\n",
		"hex key with assignment": "API key: 3f8a91c4be27d05e6a1fbc9370d24e85af610b73\n",
		"lowercase 32-char key":   "Use nkjhgfdsapoiuytrewqmnbvcxzasdfgh when connecting.\n",
	} {
		if len(ScanForCredentials(body)) == 0 {
			t.Errorf("MISS %s: %q", name, body)
		}
	}

	// ...without reintroducing the git-SHA false positive.
	if lines := ScanForCredentials("Fixed in 9f8e7d6c5b4a39281706f5e4d3c2b1a098765432 after review.\n"); len(lines) != 0 {
		t.Errorf("git SHA flagged again: %v", lines)
	}
}
