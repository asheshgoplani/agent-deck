package main

import (
	"os"
	"slices"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/sendqueue"
)

// Integration of queue control (core-22) with the guarded send (core-20-23):
// release forces --require-input-prompt and skips the readiness wait, the
// normal queued child carries neither flag, and a guarded queued send keeps
// only the guard.
func TestQueueReleaseChildForcesGuardedNoWaitSend(t *testing.T) {
	release := childSendArgs("", "target", "/tmp/x.message", releaseChildOptions)
	for _, flag := range []string{"--queue-worker", "--require-input-prompt", "--no-wait"} {
		if !slices.Contains(release, flag) {
			t.Fatalf("release child argv %v lacks %s", release, flag)
		}
	}
	normal := childSendArgs("", "target", "/tmp/x.message", childSendOptions{})
	if slices.Contains(normal, "--require-input-prompt") || slices.Contains(normal, "--no-wait") {
		t.Fatalf("normal queued child argv %v carries release flags", normal)
	}
	guarded := childSendArgs("", "target", "/tmp/x.message", childSendOptions{requireInputPrompt: true})
	if !slices.Contains(guarded, "--require-input-prompt") || slices.Contains(guarded, "--no-wait") {
		t.Fatalf("guarded queued child argv %v", guarded)
	}
}

// A released entry is marked RequireInputPrompt before its child starts, so
// when the guard refuses it untyped the requeued entry stays guarded on the
// worker's next normal attempt.
func TestQueueReleaseMarksEntryGuardedForLaterAttempts(t *testing.T) {
	dir, rec := queueControlFixture(t, sendqueue.StateQueued)
	if rec.RequireInputPrompt {
		t.Fatal("fixture starts guarded")
	}
	prevRelease, prevGuarded, prevNormal := sendChildRelease, sendChildGuarded, sendChild
	t.Cleanup(func() { sendChildRelease, sendChildGuarded, sendChild = prevRelease, prevGuarded, prevNormal })
	used := ""
	stub := func(name, result string, code int) func(string, string, string, string) (int, func() int, error) {
		return func(_, _, _, resultPath string) (int, func() int, error) {
			used = name
			stored, err := sendqueue.Load(dir, rec.SendID)
			if err != nil || !stored.RequireInputPrompt {
				t.Fatalf("%s child started on an unguarded record: %+v %v", name, stored, err)
			}
			if err := os.WriteFile(resultPath, []byte(result), 0o600); err != nil {
				t.Fatal(err)
			}
			return 4242, func() int { return code }, nil
		}
	}
	blocked := `{"success":false,"delivery":"composer_blocked","error":"no input prompt; no keys typed"}`
	sendChildRelease = stub("release", blocked, 1)
	sendChildGuarded = stub("guarded", `{"success":true,"delivery":"delivered","submitted":true,"confirmation":"confirmed"}`, 0)
	sendChild = func(string, string, string, string) (int, func() int, error) {
		t.Fatal("a released entry fell back to the unguarded child")
		return 0, nil, nil
	}
	set := func(fn func(*sendqueue.Record)) error {
		stored, err := sendqueue.Update(dir, rec.SendID, time.Now(), fn)
		if err == nil {
			*rec = *stored
		}
		return err
	}
	if !typeQueued("", dir, rec, queueReleaseStatus, "", 0, set) || used != "release" {
		t.Fatalf("release used %q", used)
	}
	if rec.State != sendqueue.StateQueued || !rec.RequireInputPrompt {
		t.Fatalf("refused release: %+v", rec)
	}
	if !typeQueued("", dir, rec, "waiting", "", 0, set) || used != "guarded" {
		t.Fatalf("next normal attempt used %q", used)
	}
	if rec.State != sendqueue.StateSubmitted {
		t.Fatalf("guarded retry: %+v", rec)
	}
}
