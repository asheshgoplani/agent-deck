package session

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

// Issue #2539: the drain must name the remote's configured profile on BOTH
// export reads, including "default" and an unset profile. The global -p is
// omitted for "default" (and the persistent channel re-execs under the -p it
// was started with), so without an explicit --profile a default remote's
// export ran under whatever profile that host resolved on its own.
func TestIssue2539_SSHRunnerSendsProfileOnBothExports(t *testing.T) {
	for _, tc := range []struct{ configured, want string }{{"", "default"}, {"default", "default"}, {"A", "A"}} {
		r := NewSSHRunner("box", RemoteConfig{Host: "worker@box", Profile: tc.configured})
		var calls [][]string
		r.runFn = func(_ context.Context, args ...string) ([]byte, error) {
			calls = append(calls, append([]string(nil), args...))
			return []byte("[]"), nil
		}
		r.runStdinFn = func(_ context.Context, _ []byte, args ...string) ([]byte, error) {
			calls = append(calls, append([]string(nil), args...))
			return []byte(`{"records":[],"cursor_next":{}}`), nil
		}
		if _, err := r.FetchPendingRecords(context.Background()); err != nil {
			t.Fatalf("profile %q: full export: %v", tc.configured, err)
		}
		if _, err := r.FetchRecordsAfter(context.Background(), RemoteCursor{}); err != nil {
			t.Fatalf("profile %q: cursor export: %v", tc.configured, err)
		}
		if len(calls) != 2 {
			t.Fatalf("profile %q: want 2 remote calls, got %v", tc.configured, calls)
		}
		for _, args := range calls {
			i := slices.Index(args, "--profile")
			if i < 0 || i+1 >= len(args) || args[i+1] != tc.want {
				t.Errorf("profile %q: export must send --profile %s: %v", tc.configured, tc.want, args)
			}
		}
	}
}

// A remote too old for --profile can only answer with every profile's records:
// the drain fails, and the cursor read does not fall back to the full export.
func TestIssue2539_OldRemoteWithoutProfileFlagFailsClosed(t *testing.T) {
	cursorTestHome(t)
	r := &SSHRunner{Host: "worker@box-b", AgentDeckPath: "agent-deck"}
	var calls []string
	SetSSHRunnerRunFnForTest(r, func(args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		if slices.Contains(args, "--profile") {
			return nil, errors.New("ssh command failed: exit status 2: flag provided but not defined: -profile")
		}
		return []byte(`{"running":true,"detail":"ok"}`), nil
	})
	if _, err := r.FetchPendingRecords(context.Background()); !errors.Is(err, ErrRemoteProfileScopeUnsupported) {
		t.Fatalf("full export: want ErrRemoteProfileScopeUnsupported, got %v", err)
	}
	if _, err := r.FetchRecordsAfter(context.Background(), RemoteCursor{}); !errors.Is(err, ErrRemoteProfileScopeUnsupported) ||
		errors.Is(err, ErrRemoteCursorUnsupported) {
		t.Fatalf("cursor export: want ErrRemoteProfileScopeUnsupported only, got %v", err)
	}
	calls = nil
	deps := RemoteTalkbackDeps{RemoteProfile: DefaultProfile, FetchAfter: r.FetchRecordsAfter, FetchAll: r.FetchPendingRecords, WriterProbe: r.FetchWriterStatus}
	if _, err := RunRemoteTalkback(context.Background(), "boxb", "conductor-old", deps); !errors.Is(err, ErrRemoteProfileScopeUnsupported) {
		t.Fatalf("drain: want a failed fetch, got %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("no fallback read may follow the rejected scope: %v", calls)
	}
}

func TestIssue2539_ExportsRefuseAnEmptyProfile(t *testing.T) {
	cursorTestHome(t)
	if _, err := ExportPendingRecords("  "); err == nil {
		t.Fatal("the full export must refuse an empty profile")
	}
	if _, err := ExportRecordsAfter("", RemoteCursor{}); err == nil {
		t.Fatal("the cursor export must refuse an empty profile")
	}
	deps := RemoteTalkbackDeps{
		FetchAfter: func(context.Context, RemoteCursor) (RemoteExport, error) {
			t.Fatal("no fetch without a profile to scope it to")
			return RemoteExport{}, nil
		},
	}
	if _, err := RunRemoteTalkback(context.Background(), "boxb", "conductor-x", deps); err == nil {
		t.Fatal("a drain without a remote profile must fail")
	}
}

func TestIssue2539_KeepProfileRecords(t *testing.T) {
	in := []TransitionNotificationEvent{
		{ChildSessionID: "a1", Profile: "A"}, {ChildSessionID: "a2", Profile: " A "},
		{ChildSessionID: "b", Profile: "B"}, {ChildSessionID: "blank"}, {ChildSessionID: "ab", Profile: "AB"},
	}
	got := KeepProfileRecords(in, "A")
	if len(got) != 2 || got[0].ChildSessionID != "a1" || got[1].ChildSessionID != "a2" {
		t.Fatalf("exact trimmed match only: %+v", got)
	}
	if got := KeepProfileRecords(in, " "); len(got) != 0 {
		t.Fatalf("an empty profile keeps nothing: %+v", got)
	}
}
