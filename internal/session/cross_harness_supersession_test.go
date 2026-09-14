package session

import (
	"context"
	"errors"
	"os"
	"testing"
)

// These failure controls prove that an unfinished cross-harness replacement
// leaves exactly one source-visible row: the archived target is durable for
// recovery but cannot appear as a second active session.
func TestCrossHarnessSupersession_EmptyPortableSourceRefusesBeforeTargetCreation(t *testing.T) {
	cfg, source := seededCrossHarnessExecutorSource(t)
	path, err := canonicalClaudeExactTranscriptPath(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"sessionId":"`+source.ClaudeSessionID+`","type":"file-history-snapshot","metadata":{"only":"native metadata"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &executorFakeStore{targets: map[string]*Instance{}}
	lifecycle := &executorFakeLifecycle{}
	_, err = ExecuteCrossHarnessSwitch(context.Background(), cfg, source, CrossHarnessSwitchOptions{Target: SwitchPreviewTarget{Harness: "codex", Account: "work"}}, CrossHarnessSwitchDependencies{Store: store, Lifecycle: lifecycle, Observer: &executorFakeObserver{}, SourceOwnership: executorSourceOwnership()})
	if err == nil || len(store.targets) != 0 || lifecycle.calls != 0 || source.IsArchived() {
		t.Fatalf("empty portable source created or launched a target: err=%v targets=%#v lifecycle=%d source=%#v", err, store.targets, lifecycle.calls, source)
	}
}

func TestCrossHarnessSupersession_PendingErrorAndCommitFailureKeepSourceVisible(t *testing.T) {
	cases := []struct {
		name      string
		opts      CrossHarnessSwitchOptions
		lifecycle *executorFakeLifecycle
		observer  *executorFakeObserver
		store     *executorFakeStore
	}{
		{
			name:  "no-start pending",
			opts:  CrossHarnessSwitchOptions{Target: SwitchPreviewTarget{Harness: "codex", Account: "work"}, NoStart: true},
			store: &executorFakeStore{targets: map[string]*Instance{}},
		},
		{
			name:      "readiness error",
			opts:      CrossHarnessSwitchOptions{Target: SwitchPreviewTarget{Harness: "codex", Account: "work"}},
			lifecycle: &executorFakeLifecycle{},
			observer:  &executorFakeObserver{err: errors.New("injected readiness failure")},
			store:     &executorFakeStore{targets: map[string]*Instance{}},
		},
		{
			name:      "final commit failure",
			opts:      CrossHarnessSwitchOptions{Target: SwitchPreviewTarget{Harness: "codex", Account: "work"}},
			lifecycle: &executorFakeLifecycle{},
			observer:  &executorFakeObserver{},
			store:     &executorFakeStore{targets: map[string]*Instance{}, finalizeErr: errors.New("injected CAS failure")},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, source := seededCrossHarnessExecutorSource(t)
			deps := CrossHarnessSwitchDependencies{Store: tc.store, Lifecycle: tc.lifecycle, Observer: tc.observer, SourceOwnership: executorSourceOwnership()}
			result, err := ExecuteCrossHarnessSwitch(context.Background(), cfg, source, tc.opts, deps)
			if err == nil || result == nil || result.Target == nil {
				t.Fatalf("unfinished switch = %#v, %v", result, err)
			}
			if source.IsArchived() || source.SupersededBy != "" {
				t.Fatalf("source was hidden before successful final commit: %#v", source)
			}
			if !result.Target.IsArchived() || result.Target.Supersedes != source.ID {
				t.Fatalf("pending/error target was not hidden with recoverable lineage: %#v", result.Target)
			}
		})
	}
}
