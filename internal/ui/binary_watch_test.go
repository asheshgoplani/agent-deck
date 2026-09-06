package ui

import (
	"errors"
	"testing"
	"time"
)

func fpAt(sec int64, size int64) binaryFingerprint {
	return binaryFingerprint{ModTime: time.Unix(sec, 0), Size: size}
}

// TestBinaryWatch_UnchangedFileNeverProbes pins the cheap path: as long as
// the on-disk fingerprint matches the running build, no probe is requested.
func TestBinaryWatch_UnchangedFileNeverProbes(t *testing.T) {
	base := fpAt(1000, 500)
	w := newBinaryWatch("/bin/agent-deck", "1.16.0", base)
	for i := 0; i < 5; i++ {
		if w.observe(base) {
			t.Fatalf("tick %d: observe requested a probe for an unchanged file", i)
		}
	}
	if got := w.installedVersion; got != "" {
		t.Fatalf("installedVersion = %q, want empty", got)
	}
}

// TestBinaryWatch_NewerVersionBecomesInstalled walks the happy path: the
// file changes, exactly one probe runs, and a newer version flips the
// watch into the installed state.
func TestBinaryWatch_NewerVersionBecomesInstalled(t *testing.T) {
	base := fpAt(1000, 500)
	replaced := fpAt(2000, 512)
	w := newBinaryWatch("/bin/agent-deck", "1.16.0", base)

	if !w.observe(replaced) {
		t.Fatal("first observe of a changed file should request a probe")
	}
	if w.observe(replaced) {
		t.Fatal("second observe should not request a probe while one is in flight")
	}
	w.recordProbe(replaced, "1.16.1", nil)
	if got := w.installedVersion; got != "1.16.1" {
		t.Fatalf("installedVersion = %q, want 1.16.1", got)
	}
	if w.observe(replaced) {
		t.Fatal("observe should be quiet once the new fingerprint was probed")
	}
}

// TestBinaryWatch_SameOrOlderVersionClears pins that a rewrite to the same
// version (reinstall) or an older one (deliberate downgrade) does not claim
// an update was installed, and clears an earlier installed state.
func TestBinaryWatch_SameOrOlderVersionClears(t *testing.T) {
	w := newBinaryWatch("/bin/agent-deck", "1.16.0", fpAt(1000, 500))
	newer := fpAt(2000, 512)
	if !w.observe(newer) {
		t.Fatal("expected probe")
	}
	w.recordProbe(newer, "1.16.2", nil)
	if w.installedVersion != "1.16.2" {
		t.Fatalf("precondition: installedVersion = %q", w.installedVersion)
	}

	for _, tc := range []struct {
		name    string
		fp      binaryFingerprint
		version string
	}{
		{"same version", fpAt(3000, 500), "1.16.0"},
		{"older version", fpAt(4000, 400), "1.15.9"},
		{"v-prefixed same", fpAt(5000, 500), "v1.16.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !w.observe(tc.fp) {
				t.Fatal("expected probe for changed fingerprint")
			}
			w.recordProbe(tc.fp, tc.version, nil)
			if w.installedVersion != "" {
				t.Fatalf("installedVersion = %q, want empty", w.installedVersion)
			}
		})
	}
}

// TestBinaryWatch_ProbeFailureRetriesThenGivesUp pins the retry budget: a
// failing probe is retried up to binaryProbeMaxFailures times for the same
// fingerprint, then ignored until the file changes again.
func TestBinaryWatch_ProbeFailureRetriesThenGivesUp(t *testing.T) {
	w := newBinaryWatch("/bin/agent-deck", "1.16.0", fpAt(1000, 500))
	broken := fpAt(2000, 10)
	for i := 0; i < binaryProbeMaxFailures; i++ {
		if !w.observe(broken) {
			t.Fatalf("attempt %d: expected a retry", i)
		}
		w.recordProbe(broken, "", errors.New("exec format error"))
	}
	if w.observe(broken) {
		t.Fatal("expected no further probes after the retry budget is spent")
	}
	if w.installedVersion != "" {
		t.Fatalf("installedVersion = %q, want empty after failures", w.installedVersion)
	}

	fixed := fpAt(3000, 512)
	if !w.observe(fixed) {
		t.Fatal("a new fingerprint should reset the retry budget")
	}
	w.recordProbe(fixed, "1.16.1", nil)
	if w.installedVersion != "1.16.1" {
		t.Fatalf("installedVersion = %q, want 1.16.1", w.installedVersion)
	}
}

// TestBinaryWatch_NilIsInert makes sure a Home without a resolved
// executable (startBinaryWatch returned nil) behaves like the old TUI.
func TestBinaryWatch_NilIsInert(t *testing.T) {
	var w *binaryWatch
	if w.observe(fpAt(1, 1)) {
		t.Fatal("nil watch requested a probe")
	}
	w.recordProbe(fpAt(1, 1), "9.9.9", nil)
	h := &Home{}
	if h.pollBinaryChange() != nil {
		t.Fatal("pollBinaryChange on a Home without a watch should be a no-op")
	}
	if h.installedUpdateVersion() != "" {
		t.Fatal("installedUpdateVersion without a watch should be empty")
	}
}

func TestParseVersionOutput(t *testing.T) {
	cases := map[string]string{
		"Agent Deck v1.16.1":                             "1.16.1",
		"Agent Deck v1.16.1 (update available: v1.16.2)": "1.16.1",
		"Agent Deck v1.17.0-rc.1":                        "1.17.0-rc.1",
		"garbage":                                        "",
		"":                                               "",
		"warning: something\nAgent Deck v1.16.3\nmore text": "1.16.3",
	}
	for in, want := range cases {
		if got := parseVersionOutput(in); got != want {
			t.Errorf("parseVersionOutput(%q) = %q, want %q", in, got, want)
		}
	}
}
