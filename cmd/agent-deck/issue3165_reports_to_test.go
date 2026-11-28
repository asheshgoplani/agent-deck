package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Issue #3165: a coordinator that is itself a sub-session launches a worker.
// Parent links stay one level deep, so the worker cannot be the
// coordinator's tree child; it reports to the coordinator instead
// (Instance.ReportsTo). These tests pin who the worker reports to on every
// launch path, that the coordinator lists it in `session children`, that the
// root does not, and that a launch from a human shell is unchanged.

// The fixture's child is the coordinator (a sub-session of parent, the root).

func TestIssue3165_SelectorRecordsTheLauncherOnEveryPathFromASubsession(t *testing.T) {
	f := newParentFixture()
	for _, tc := range []struct {
		name       string
		caller     string
		noParent   bool
		nest       bool
		wantParent *session.Instance
		wantBy     *session.Instance
	}{
		{"sub-session caller, nest off (default)", f.child.ID, false, nestOff, nil, f.child},
		{"sub-session caller, nest on", f.child.ID, false, nestOn, f.parent, f.child},
		{"sub-session caller --no-parent, nest off: a peer", f.child.ID, true, nestOff, nil, nil},
		{"sub-session caller --no-parent, nest on: a peer", f.child.ID, true, nestOn, nil, nil},
		{"top-level caller is the parent, no launcher", f.parent.ID, false, nestOff, f.parent, nil},
		{"human shell (no identity)", "", false, nestOff, nil, nil},
		{"human shell (no identity), nest on", "", false, nestOn, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setCaller(t, tc.caller)
			p, by, _, err := selectLaunchParent("", tc.noParent, tc.nest, f.all)
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if p != tc.wantParent || by != tc.wantBy {
				t.Fatalf("got parent=%s launched-by=%s, want parent=%s launched-by=%s",
					titleOf(p), titleOf(by), titleOf(tc.wantParent), titleOf(tc.wantBy))
			}
		})
	}
}

// A sub-session whose own parent is gone still reports to the launcher.
func TestIssue3165_DanglingParentCallerStillReportsToTheLauncher(t *testing.T) {
	f := newParentFixture()
	orphan := session.NewInstanceWithGroupAndTool("Orphan", "/proj/o", "g", "claude")
	orphan.ID = "orp-0001"
	orphan.SetParentWithPath("gone-0001", "/proj/gone")
	setCaller(t, orphan.ID)
	for _, nest := range []bool{nestOff, nestOn} {
		p, by, _, err := selectLaunchParent("", false, nest, append(f.all, orphan))
		if err != nil || p != nil || by != orphan {
			t.Fatalf("nest=%v: got parent=%s launched-by=%s err=%v, want top-level reporting to Orphan", nest, titleOf(p), titleOf(by), err)
		}
	}
}

func TestIssue3165_ChildrenListsReportsToWorkersMarkedWithTheirLauncher(t *testing.T) {
	f := newParentFixture()
	topWorker := session.NewInstanceWithGroupAndTool("TopWorker", "/proj/w1", "g", "claude")
	topWorker.ID = "wrk-0001"
	topWorker.SetReportsTo(f.child.ID) // default path: top-level
	nestedWorker := session.NewInstanceWithGroupAndTool("NestedWorker", "/proj/w2", "g", "claude")
	nestedWorker.ID = "wrk-0002"
	nestedWorker.SetParentWithPath(f.parent.ID, f.parent.ProjectPath) // nest_under_parent path
	nestedWorker.SetReportsTo(f.child.ID)
	all := append(f.all, topWorker, nestedWorker)

	coord := childrenOf(f.child.ID, all)
	if len(coord) != 2 || coord[0] != topWorker || coord[1] != nestedWorker {
		t.Fatalf("coordinator children = %v, want both workers", ids(coord))
	}
	// The root still lists its tree children, the nested worker included,
	// and its row says the records go to the coordinator.
	root := childrenOf(f.parent.ID, all)
	if len(root) != 2 || root[0] != f.child || root[1] != nestedWorker {
		t.Fatalf("root children = %v, want the coordinator and the nested worker", ids(root))
	}
	for _, r := range buildChildRows(coord, cachedChildStatus) {
		if r.ReportsTo != f.child.ID {
			t.Fatalf("row %s reports_to %q, want the coordinator", r.ID, r.ReportsTo)
		}
	}
	want := map[string]string{f.child.ID: "", nestedWorker.ID: f.child.ID}
	for _, r := range buildChildRows(root, cachedChildStatus) {
		if r.ReportsTo != want[r.ID] {
			t.Fatalf("root row %s reports_to %q, want %q", r.ID, r.ReportsTo, want[r.ID])
		}
	}
	if got := childrenOf("", all); len(got) != 0 {
		t.Fatalf("childrenOf(\"\") = %v, want none", ids(got))
	}
}

// The worker's kickoff is the coordinator asking, so it carries the
// coordinator's envelope (issue #3165, step 3), on both launch paths.
func TestIssue3165_KickoffTaggedWithTheLaunchersEnvelope(t *testing.T) {
	root := &session.Instance{ID: "root-1", Tool: "claude"}
	coord := &session.Instance{ID: "coord-1", Tool: "claude", ParentSessionID: "root-1"}
	const msg = "print the done line"
	for _, tc := range []struct {
		name               string
		sender             string
		parent, launchedBy *session.Instance
		want               bool
	}{
		{"default path: top-level, reports to the coordinator", "coord-1", nil, coord, true},
		{"nested path: under the root, reports to the coordinator", "coord-1", root, coord, true},
		{"top-level caller is the parent (#3131 unchanged)", "root-1", root, nil, true},
		{"human shell", "", nil, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, tagged := tagLaunchKickoff(msg, launchKickoffTagInputs{
				senderID: tc.sender, parent: launchReportTarget(tc.parent, tc.launchedBy),
				childTool: "claude", enabled: true, childReportsTurns: true,
			})
			if tagged != tc.want {
				t.Fatalf("tagged = %v, want %v", tagged, tc.want)
			}
			if tagged && session.PromptSender(got) != tc.sender {
				t.Fatalf("sender %q, want %q", session.PromptSender(got), tc.sender)
			}
		})
	}
}

// `add` from inside a sub-session persists the launcher, default config.
func TestIssue3165_AddFromSubsessionPersistsReportsTo(t *testing.T) {
	for _, nest := range []bool{nestOff, nestOn} {
		t.Run(map[bool]string{nestOff: "nest off", nestOn: "nest on"}[nest], func(t *testing.T) {
			home, _, profile := setupAddDefaultPathTest(t)
			parentPath := filepath.Join(home, "parent-project")
			if err := os.MkdirAll(parentPath, 0o755); err != nil {
				t.Fatal(err)
			}
			f := seedParentRegistry(t, profile, parentPath)
			if nest {
				writeAddUserConfig(t, home, nestUnderParentConfig)
			}
			setCaller(t, f.child.ID)
			captureStderr(t, func() {
				captureStdout(t, func() { handleAdd(profile, []string{"--title", "helper", "--quiet"}) })
			})
			helper, hints := loadAddedHelper(t, profile)
			if helper.ReportsTo != f.child.ID || helper.ReportTargetID() != f.child.ID {
				t.Fatalf("helper reports_to=%q target=%q, want the coordinator %q", helper.ReportsTo, helper.ReportTargetID(), f.child.ID)
			}
			if hints[hintKeyLaunchedBy] != f.child.ID {
				t.Fatalf("hints %v, want launched-by=%s", hints, f.child.ID)
			}
		})
	}
}

// From a human shell nothing is recorded.
func TestIssue3165_AddFromShellRecordsNoLauncher(t *testing.T) {
	_, _, profile := setupAddDefaultPathTest(t)
	seedParentRegistry(t, profile, "")
	setCaller(t, "")
	captureStderr(t, func() {
		captureStdout(t, func() { handleAdd(profile, []string{"--title", "helper", "--quiet"}) })
	})
	helper, hints := loadAddedHelper(t, profile)
	if helper.ReportsTo != "" || helper.ParentSessionID != "" {
		t.Fatalf("shell add: reports_to=%q parent=%q, want neither", helper.ReportsTo, helper.ParentSessionID)
	}
	if _, ok := hints[hintKeyLaunchedBy]; ok {
		t.Fatalf("shell add carries launched-by: %v", hints)
	}
}

// Only a Claude-compatible launcher is reported to: it drains its inbox at
// prompt time and can be woken. A shell sub-session is a person at a prompt
// and another harness would hold records nobody reads, so those keep the old
// routing (the tree parent under nest_under_parent, else none).
func TestIssue3165_OnlyAClaudeLauncherIsReportedTo(t *testing.T) {
	root := &session.Instance{ID: "root-1", Tool: "claude"}
	for _, tc := range []struct {
		tool string
		want bool
	}{{"claude", true}, {"shell", false}, {"codex", false}} {
		by := &session.Instance{ID: "coord-1", Tool: tc.tool, ParentSessionID: "root-1"}
		if got := reportsToLauncher(by) != nil; got != tc.want {
			t.Fatalf("%s launcher reported to = %v, want %v", tc.tool, got, tc.want)
		}
		wantTarget := root
		if tc.want {
			wantTarget = by
		}
		if got := launchReportTarget(root, by); got != wantTarget {
			t.Fatalf("%s launcher: report target %s, want %s", tc.tool, got.ID, wantTarget.ID)
		}
	}
	if reportsToLauncher(nil) != nil {
		t.Fatal("no launcher reported to")
	}
}
