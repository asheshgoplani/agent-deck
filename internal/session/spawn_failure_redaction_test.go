package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A spawn failure is later exposed by session show --json, so new sidecars must
// never retain credential values even when the sidecar is already 0600.
func TestSpawnFailureRecord_RedactsCredentialsAtPersistenceBoundary(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runtime", "spawn-failure")
	markers := []string{"otel-bearer-marker", "api-key-marker", "basic-marker", "token-marker"}
	rec := SpawnFailureRecord{
		InstanceID: "leaktest",
		Tool:       "generic",
		Command: "cd /safe/recovery && " +
			"export OTEL_EXPORTER_OTLP_TRACES_HEADERS='Authorization=Bearer otel-bearer-marker' && " +
			"mytool --api-key=\"api-key-marker\" --session-id session-keep",
		Reason:      "prepare_failed",
		DyingOutput: "upstream rejected Authorization: Basic basic-marker\nexport ACCESS_TOKEN='token-marker'",
	}
	if err := writeSpawnFailureRecordTo(rec, dir); err != nil {
		t.Fatalf("write: %v", err)
	}

	path := filepath.Join(dir, "leaktest.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	for _, marker := range markers {
		if strings.Contains(string(data), marker) {
			t.Fatalf("credential marker leaked into sidecar: %s", marker)
		}
	}
	var got SpawnFailureRecord
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !strings.Contains(got.Command, "OTEL_EXPORTER_OTLP_TRACES_HEADERS='[redacted]'") ||
		!strings.Contains(got.Command, `--api-key="[redacted]"`) {
		t.Fatalf("credential-bearing command values were not redacted: %s", got.Command)
	}
	if !strings.Contains(got.Command, "cd /safe/recovery") ||
		!strings.Contains(got.Command, "mytool") ||
		!strings.Contains(got.Command, "--session-id session-keep") {
		t.Fatalf("non-secret command identity did not survive: %s", got.Command)
	}
	if !strings.Contains(got.DyingOutput, "Authorization: Basic [redacted]") ||
		!strings.Contains(got.DyingOutput, "ACCESS_TOKEN='[redacted]'") {
		t.Fatalf("credential-bearing output was not redacted: %s", got.DyingOutput)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("sidecar perms = %o, want 600", info.Mode().Perm())
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("spawn-failure dir perms = %o, want 700", dirInfo.Mode().Perm())
	}
}

func TestRedactSpawnFailureDiagnostic_CoversBoundedCredentialForms(t *testing.T) {
	cases := []struct {
		name      string
		input     string
		secret    string
		contains  string
		unchanged bool
	}{
		{
			name:      "normal command preserved",
			input:     "cd /recovery/session-42 && codex resume session-keep",
			contains:  "codex resume session-keep",
			unchanged: true,
		},
		{
			name:     "shell escaped API key",
			input:    `export API_KEY='api-key-marker'\''suffix' && mytool run`,
			secret:   "api-key-marker",
			contains: "export API_KEY='[redacted]' && mytool run",
		},
		{
			name:     "multiline token",
			input:    "TOKEN='token-marker\nsecond-line'\nmytool run",
			secret:   "token-marker",
			contains: "TOKEN='[redacted]'",
		},
		{
			name:     "basic authorization header",
			input:    `-H 'Authorization: Basic basic-marker' mytool`,
			secret:   "basic-marker",
			contains: "Authorization: Basic [redacted]",
		},
		{
			name:     "bearer authorization header",
			input:    `Authorization="Bearer bearer-marker" mytool`,
			secret:   "bearer-marker",
			contains: `Authorization="Bearer [redacted]"`,
		},
		{
			name:     "malformed quote remains safe",
			input:    "export ACCESS_TOKEN='token-marker unterminated",
			secret:   "token-marker",
			contains: "ACCESS_TOKEN='[redacted]'",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := redactSpawnFailureDiagnostic(tc.input)
			if tc.unchanged && got != tc.input {
				t.Fatalf("normal command changed: got %q want %q", got, tc.input)
			}
			if tc.secret != "" && strings.Contains(got, tc.secret) {
				t.Fatalf("credential marker leaked: %q", got)
			}
			if !strings.Contains(got, tc.contains) {
				t.Fatalf("redacted diagnostic = %q, missing %q", got, tc.contains)
			}
		})
	}
}

// Older sidecars are evidence and must not be rewritten or deleted. Their
// presentation is still redacted before an operator can copy it into an issue.
func TestSpawnFailureRecord_LegacyEvidenceIsUnchangedAndDisplayIsRedacted(t *testing.T) {
	legacy := &SpawnFailureRecord{
		InstanceID:  "legacy-session",
		Command:     "mytool -H 'Authorization: Bearer legacy-bearer-marker'",
		Reason:      "prepare_failed",
		DyingOutput: "export API_KEY='legacy-key-marker' failed",
	}
	before := *legacy
	display := legacy.FormatForDisplay()
	if strings.Contains(display, "legacy-bearer-marker") || strings.Contains(display, "legacy-key-marker") {
		t.Fatalf("legacy credential leaked through display: %s", display)
	}
	if !strings.Contains(display, "Authorization: Bearer [redacted]") ||
		!strings.Contains(display, "API_KEY='[redacted]'") {
		t.Fatalf("legacy display was not redacted: %s", display)
	}
	if *legacy != before {
		t.Fatalf("display mutated legacy evidence: got %+v want %+v", legacy, before)
	}
}
