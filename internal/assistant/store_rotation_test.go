package assistant

import (
	"os"
	"strings"
	"testing"
)

// Rotating a token keeps the old token file until the new record is saved:
// a failed rotation leaves the old token working and in its file.
func TestStore_FailedRotationKeepsTokenFile(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	oldTok, _, err := s.Create("bot", "mcp", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	failRecordsWrite(t, s)
	if _, _, err := s.Create("bot", "mcp", AllScopes, ""); err == nil {
		t.Fatal("rotation with a failing save reported success")
	}
	got, err := ReadTokenFile(s.TokenFile("bot"))
	if err != nil {
		t.Fatal(err)
	}
	if got != oldTok {
		t.Fatalf("token file holds %q after a failed rotation, want the old token", got)
	}
	if _, err := NewStore(dir).Authenticate(got); err != nil {
		t.Fatalf("the token in the file no longer authenticates: %v", err)
	}
	entries, _ := os.ReadDir(s.Dir() + "/assistants")
	for _, e := range entries {
		if e.Name() != "bot.token" && !strings.HasSuffix(e.Name(), ".lock") {
			t.Errorf("stray file left in the token dir: %s", e.Name())
		}
	}
}
