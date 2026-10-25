package session_test

import (
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/core"
	"github.com/asheshgoplani/agent-deck/internal/sendqueue"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// The queued message goes to the store's own profile. A store whose directory
// is not named after its profile (an explicit path, tests) must not send it
// to a profile guessed from the directory name.
func TestQueuedMessageUsesTheStoresProfile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	storage := session.NewTestStorageForExternalTests(t)
	var profiles []string
	t.Cleanup(core.SetSendWorkerSpawnForTest(func(profile, _ string) error {
		profiles = append(profiles, profile)
		return nil
	}))
	t.Cleanup(session.SetQueuedSpawnForTest(func(i *session.Instance, _ string) error {
		i.Status = session.StatusStarting
		return nil
	}))
	inst := session.NewInstanceWithGroup("worker", t.TempDir(), "workers")
	inst.Tool = "shell"
	inst.MarkQueued("brief")
	if err := storage.SaveWithGroups([]*session.Instance{inst}, nil); err != nil {
		t.Fatal(err)
	}

	if _, err := storage.StartQueued(inst); err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 1 || profiles[0] != storage.Profile() {
		t.Fatalf("send worker started for profiles %q, want [%q]", profiles, storage.Profile())
	}
	recs, err := sendqueue.List(sendqueue.Dir(filepath.Dir(storage.Path())), inst.ID)
	if err != nil || len(recs) != 1 || recs[0].Message != "brief" {
		t.Fatalf("queued sends next to the store = %+v (err %v), want one \"brief\"", recs, err)
	}
}
