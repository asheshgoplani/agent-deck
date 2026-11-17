package main

import (
	"strings"
	"testing"
)

// PR #3166 review (tarekrached, ntmspavan): a default (top-level) worker
// launched from a sub-session has reports_to and no tree parent.
// `session unset-parent` must detach it from its launcher instead of
// refusing with "is not a sub-session".
func TestIssue3165_UnsetParentDetachesATopLevelWorker(t *testing.T) {
	_, _, profile := setupAddDefaultPathTest(t)
	f := seedParentRegistry(t, profile, "")
	setCaller(t, f.child.ID)
	captureStderr(t, func() {
		captureStdout(t, func() { handleAdd(profile, []string{"--title", "helper", "--quiet"}) })
	})
	helper, _ := loadAddedHelper(t, profile)
	if helper.ParentSessionID != "" || helper.ReportsTo != f.child.ID {
		t.Fatalf("setup: parent=%q reports_to=%q, want a top-level worker reporting to %q", helper.ParentSessionID, helper.ReportsTo, f.child.ID)
	}

	exitCode := -1
	var out string
	func() {
		cliExitHook = func(code int) { panic(code) }
		defer func() {
			cliExitHook = nil
			if r := recover(); r != nil {
				c, ok := r.(int)
				if !ok {
					panic(r)
				}
				exitCode = c
			}
		}()
		out = captureStdout(t, func() { handleSessionUnsetParent(profile, []string{helper.ID}) })
	}()
	if exitCode != -1 {
		t.Fatalf("unset-parent on a top-level worker exited %d, want success (stdout %q)", exitCode, out)
	}
	if !strings.Contains(out, f.child.Title) {
		t.Fatalf("output %q does not name the launcher %q it was detached from", out, f.child.Title)
	}
	helper, _ = loadAddedHelper(t, profile)
	if helper.ReportsTo != "" || helper.ReportTargetID() != "" {
		t.Fatalf("after unset-parent reports_to=%q target=%q, want both cleared", helper.ReportsTo, helper.ReportTargetID())
	}
}

// A session with neither link is still refused.
func TestIssue3165_UnsetParentRefusesAnUnlinkedSession(t *testing.T) {
	_, _, profile := setupAddDefaultPathTest(t)
	seedParentRegistry(t, profile, "")
	setCaller(t, "")
	captureStderr(t, func() {
		captureStdout(t, func() { handleAdd(profile, []string{"--title", "helper", "--quiet"}) })
	})
	helper, _ := loadAddedHelper(t, profile)
	exitCode := -1
	func() {
		cliExitHook = func(code int) { panic(code) }
		defer func() {
			cliExitHook = nil
			if r := recover(); r != nil {
				exitCode = r.(int)
			}
		}()
		captureStderr(t, func() {
			captureStdout(t, func() { handleSessionUnsetParent(profile, []string{helper.ID}) })
		})
	}()
	if exitCode != 1 {
		t.Fatalf("unset-parent on an unlinked session exited %d, want 1", exitCode)
	}
}
