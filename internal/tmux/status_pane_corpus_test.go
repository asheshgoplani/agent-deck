package tmux

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The status pane corpus (testdata/status-corpus) holds the pane captured in
// every status-detection report: <tool>/<case>.txt plus one corpus.json entry
// naming the tool, the frame-only verdict and the source issue. See its
// README.md for how to add a capture.

const statusPaneCorpusDir = "testdata/status-corpus"

type statusPaneCorpusEntry struct {
	File     string `json:"file"`
	Tool     string `json:"tool"`
	Expected string `json:"expected"`
	Substate string `json:"substate,omitempty"`
	Source   string `json:"source"`
	Note     string `json:"note"`
}

// statusCorpusDetectorTools is every tool with built-in pane patterns
// (DefaultRawPatterns). Each one's detector is run over every corpus frame.
var statusCorpusDetectorTools = []string{
	"claude", "codewhale", "codex", "copilot", "cursor", "deepseek", "gemini",
	"muse", "omp", "opencode", "openclaw", "pi", "shell",
}

// statusCorpusForeignRunning lists the detectors that do read some other
// tool's idle frame as running today, with the reason. These are open gaps,
// not accepted behaviour: a pane's history can hold any text, so a detector
// that fires on quoted prose is the #2351 bug for that tool. Each listed
// frame must still fire (the test fails when it stops), so the list only
// shrinks; a new frame that fires fails the test.
type statusCorpusKnownGap struct {
	why    string
	frames []string
}

const (
	quotedInterrupt = "plain interrupt phrase in prose near the bottom counts as live (the #2351 shape; only Codex requires live provenance)"
	frozenSpinner   = "a spinner line frozen above an open Claude menu counts as live (only the Claude detector lets the menu outrank it)"
)

var statusCorpusForeignRunning = map[string]statusCorpusKnownGap{
	"claude": {
		why: quotedInterrupt,
		frames: []string{
			"codex/issue2351-quoted-example-bare-prompt.txt",
			"codex/issue2351-quoted-prose-composer.txt",
		},
	},
	"codewhale": {
		why: "busy string \"working (\" matches Codex status lines quoted in history; " + frozenSpinner,
		frames: []string{
			"codex/issue2351-quoted-example-bare-prompt.txt",
			"status_corpus/claude-local-meeting-watcher_33b50278",
			"status_corpus/codex-idle-historical-status-then-rule",
			"status_corpus/codex-idle-quoted-status-continuation",
			"status_corpus/synth-codex-ran-grep-for-status-line",
			"status_corpus/synth-codex-user-pasted-working",
		},
	},
	"codex": {
		why: frozenSpinner,
		frames: []string{
			"status_corpus/claude-local-meeting-watcher_33b50278",
		},
	},
	"copilot": {
		why: "plain busy words (Running, Reading, Searching, thinking, esc to interrupt) anywhere in the last 25 lines; no Copilot capture yet",
		frames: []string{
			"codex/issue2351-quoted-example-bare-prompt.txt",
			"codex/issue2351-quoted-prose-composer.txt",
			"status_corpus/claude-local-conductor-personal_4b0ff296",
			"status_corpus/claude-local-diskspace_0049a9c0",
			"status_corpus/claude-local-meeting-watcher_33b50278",
			"status_corpus/claude-local-review-2348-r2_43b89855",
			"status_corpus/claude-local-sessions_81cde3d6",
			"status_corpus/claude-local-whatsapp-watcher_71e154e9",
			"status_corpus/claude-remote-agentbox-conductor-proj-a-owner_b203faa8",
			"status_corpus/claude-remote-agentbox-conductor-proj-a-worker-b_fc50c8f5",
			"status_corpus/claude-remote-agentbox-worker-b-r6-bug07_b3bb8b90",
			"status_corpus/claude-remote-agentbox-worker-b-r6-bug50b_f2511f00",
			"status_corpus/claude-remote-agentbox-worker-b-tutorial-redirect_59ff68f2",
			"status_corpus/claude-remote-agentbox-worker-c-mls-central-list_91138284",
			"status_corpus/claude-remote-proj-dbox-conductor-proj-d-worker-c_afd81d46",
			"status_corpus/codex-idle-ctrlc-prose",
			"status_corpus/codex-idle-quoted-status-continuation",
			"status_corpus/pi-remote-agentbox-no-cache-field",
			"status_corpus/synth-codex-agent-quotes-status-line",
			"status_corpus/synth-codex-user-pasted-working",
		},
	},
	"cursor": {
		why: quotedInterrupt + "; plain busy word \"thinking\" matches the pi footer \"thinking off\"; " + frozenSpinner,
		frames: []string{
			"codex/issue2351-quoted-example-bare-prompt.txt",
			"codex/issue2351-quoted-prose-composer.txt",
			"status_corpus/claude-local-meeting-watcher_33b50278",
			"status_corpus/pi-remote-agentbox-no-cache-field",
		},
	},
	"deepseek": {
		why: quotedInterrupt + "; " + frozenSpinner,
		frames: []string{
			"codex/issue2351-quoted-example-bare-prompt.txt",
			"codex/issue2351-quoted-prose-composer.txt",
			"status_corpus/claude-local-meeting-watcher_33b50278",
		},
	},
	"gemini": {
		why: "busy string \"esc to cancel\" matches Claude menu footers (Esc to cancel)",
		frames: []string{
			"status_corpus/claude-local-codex-bar_e2e1c2e8",
			"status_corpus/claude-local-fix_3e3ec13e",
			"status_corpus/claude-local-fork-eval_de8f6582",
			"status_corpus/claude-local-fork-submodule_cd203d56",
			"status_corpus/claude-local-meeting-watcher_33b50278",
			"status_corpus/claude-local-tets_8d1c8e35",
			"status_corpus/claude-remote-agentbox-LEDGER-proj-a_fd8d422e",
		},
	},
	"muse": {
		why: quotedInterrupt + "; " + frozenSpinner,
		frames: []string{
			"codex/issue2351-quoted-example-bare-prompt.txt",
			"codex/issue2351-quoted-prose-composer.txt",
			"status_corpus/claude-local-meeting-watcher_33b50278",
		},
	},
	"openclaw": {
		why: frozenSpinner,
		frames: []string{
			"status_corpus/claude-local-meeting-watcher_33b50278",
		},
	},
	"pi": {
		why: quotedInterrupt,
		frames: []string{
			"codex/issue2351-quoted-example-bare-prompt.txt",
			"codex/issue2351-quoted-prose-composer.txt",
		},
	},
	"shell": {
		why: frozenSpinner,
		frames: []string{
			"status_corpus/claude-local-meeting-watcher_33b50278",
		},
	},
}

func (g statusCorpusKnownGap) has(name string) bool {
	for _, f := range g.frames {
		if f == name {
			return true
		}
	}
	return false
}

var statusCorpusSubstates = map[Substate]bool{
	SubstateRunning: true, SubstateIdleAtEmptyPrompt: true, SubstateInteractiveMenu: true,
	SubstateBackgroundWork: true, SubstateWatching: true, SubstateModelUnavailable: true,
	SubstateAuth401: true, SubstateUsageLimit: true, SubstateModelLimit: true,
	SubstateAccountBlocked: true, SubstateUnknownExit: true, SubstateProcessExited: true,
	SubstateHookLag: true,
}

// classifyCorpusSubstate is the substate GetSubstate reaches for one frame,
// with the same per-tool frame preparation as ClassifyPaneFrame. The status
// light reads it on top of the frame verdict: #2502 was a live spinner the
// frame verdict saw as busy while the substate said idle at an empty prompt.
func classifyCorpusSubstate(tool, content string) Substate {
	s := &Session{DisplayName: "frame", detectedTool: tool}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.classifyFrameLocked(s.prepareFrame(StripANSI(content)))
}

var statusCorpusSourceRe = regexp.MustCompile(`^#[0-9]+$`)

func loadStatusPaneCorpus(t *testing.T) []statusPaneCorpusEntry {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(statusPaneCorpusDir, "corpus.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var entries []statusPaneCorpusEntry
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&entries); err != nil {
		t.Fatalf("parse corpus.json: %v", err)
	}
	return entries
}

// TestStatusPaneCorpusManifest keeps the corpus and its manifest in step: a
// capture without an entry is never checked, and an entry without a capture
// checks nothing.
func TestStatusPaneCorpusManifest(t *testing.T) {
	entries := loadStatusPaneCorpus(t)
	known := map[string]bool{}
	for _, tool := range statusCorpusDetectorTools {
		known[tool] = true
	}
	listed := map[string]bool{}
	for _, e := range entries {
		if listed[e.File] {
			t.Errorf("corpus.json lists %s twice", e.File)
		}
		listed[e.File] = true
		if !known[e.Tool] {
			t.Errorf("%s: tool %q has no built-in detector", e.File, e.Tool)
		}
		if dir := strings.SplitN(e.File, "/", 2)[0]; dir != e.Tool {
			t.Errorf("%s: file must live under %s/", e.File, e.Tool)
		}
		switch FrameVerdict(e.Expected) {
		case FrameActive, FrameWaiting, FrameError:
		default:
			t.Errorf("%s: expected %q, want active, waiting or error", e.File, e.Expected)
		}
		if e.Substate != "" && !statusCorpusSubstates[Substate(e.Substate)] {
			t.Errorf("%s: unknown substate %q", e.File, e.Substate)
		}
		if !statusCorpusSourceRe.MatchString(e.Source) {
			t.Errorf("%s: source %q, want the issue number as #N", e.File, e.Source)
		}
		if _, err := os.Stat(filepath.Join(statusPaneCorpusDir, filepath.FromSlash(e.File))); err != nil {
			t.Errorf("corpus.json lists %s but the capture is missing: %v", e.File, err)
		}
	}
	err := filepath.WalkDir(statusPaneCorpusDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(statusPaneCorpusDir, path)
		rel = filepath.ToSlash(rel)
		if rel == "corpus.json" || rel == "README.md" {
			return nil
		}
		if !listed[rel] {
			t.Errorf("%s has no corpus.json entry", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range statusCorpusDetectorTools {
		if DefaultRawPatterns(tool) == nil {
			t.Errorf("detector list names %q, which has no built-in patterns", tool)
		}
	}
}

type statusCorpusFrame struct {
	name, tool, expected string
	substate             string
	strict               bool // own-tool verdict is enforced here (audit frames are scored by TestStatusCorpus_FrameVerdicts)
	content              string
}

func statusCorpusFrames(t *testing.T) []statusCorpusFrame {
	t.Helper()
	var frames []statusCorpusFrame
	for _, e := range loadStatusPaneCorpus(t) {
		raw, err := os.ReadFile(filepath.Join(statusPaneCorpusDir, filepath.FromSlash(e.File)))
		if err != nil {
			t.Fatal(err)
		}
		frames = append(frames, statusCorpusFrame{e.File, e.Tool, e.Expected, e.Substate, true, string(raw)})
	}
	for _, c := range loadStatusCorpus(t) {
		raw, err := os.ReadFile(filepath.Join("testdata", "status_corpus", c.file+".txt"))
		if err != nil {
			t.Fatal(err)
		}
		frames = append(frames, statusCorpusFrame{"status_corpus/" + c.file, c.tool, c.expected, "", false, string(raw)})
	}
	return frames
}

// TestStatusPaneCorpus runs every tool's detector over every corpus frame.
// The frame's own tool must reach the expected verdict (and substate, when the
// entry names one), and no other tool may read a frame that is idle (waiting
// or error) as running.
func TestStatusPaneCorpus(t *testing.T) {
	frames := statusCorpusFrames(t)
	seen := map[string]map[string]bool{}
	for _, f := range frames {
		if f.substate != "" {
			if got := classifyCorpusSubstate(f.tool, f.content); string(got) != f.substate {
				t.Errorf("%s: %s substate is %q, want %q", f.name, f.tool, got, f.substate)
			}
		}
		for _, tool := range statusCorpusDetectorTools {
			got := ClassifyPaneFrame(tool, f.content)
			if tool == f.tool {
				if f.strict && string(got) != f.expected {
					t.Errorf("%s: %s detector says %s, want %s", f.name, tool, got, f.expected)
				}
				continue
			}
			if f.expected == string(FrameActive) || got != FrameActive {
				continue
			}
			if statusCorpusForeignRunning[tool].has(f.name) {
				if seen[tool] == nil {
					seen[tool] = map[string]bool{}
				}
				seen[tool][f.name] = true
				continue
			}
			t.Errorf("%s: %s idle frame (%s) reads as running to the %s detector", f.name, f.tool, f.expected, tool)
		}
	}
	tools := make([]string, 0, len(statusCorpusForeignRunning))
	for tool := range statusCorpusForeignRunning {
		tools = append(tools, tool)
	}
	sort.Strings(tools)
	for _, tool := range tools {
		for _, name := range statusCorpusForeignRunning[tool].frames {
			if !seen[tool][name] {
				t.Errorf("%s detector no longer reads %s as running (gap: %s): drop it from statusCorpusForeignRunning", tool, name, statusCorpusForeignRunning[tool].why)
			}
		}
	}
}
