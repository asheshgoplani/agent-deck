package needsme

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestDefaultSourcesUnreadableProfileIsAnError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permissions are not enforced for root")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("AGENTDECK_PROFILE", "")
	dir, err := session.GetProfilesDir()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"good", "locked"} {
		if err := os.MkdirAll(filepath.Join(dir, p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, p, "state.db"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(filepath.Join(dir, "locked"), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(dir, "locked"), 0o700) })

	srcs := DefaultSources("/bin/true", "default")
	res := Collect(context.Background(), Options{Sources: srcs, Timeout: time.Second, Now: fixed})
	var sawGood bool
	for _, s := range srcs {
		if s.Profile == "good" {
			sawGood = true
		}
	}
	if !sawGood {
		t.Errorf("readable profile was not asked: %+v", srcs)
	}
	if len(res.Errors) == 0 {
		t.Fatalf("unreadable profile dir must be an errors entry, got none")
	}
	for _, e := range res.Errors {
		if e.Host != "" || e.Error == "" {
			t.Errorf("bad entry %+v", e)
		}
	}
}
