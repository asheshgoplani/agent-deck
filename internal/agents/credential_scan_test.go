package agents

import "testing"

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
