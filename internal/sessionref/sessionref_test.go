package sessionref_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/sessionref"
)

func inst(id, title, path string, st session.Status) *session.Instance {
	return &session.Instance{ID: id, Title: title, ProjectPath: path, Tool: "claude", Status: st}
}

type fakeInsert struct {
	calls   []string
	targets []string
	err     error
}

func (f *fakeInsert) fn(target *session.Instance, line string) error {
	f.calls = append(f.calls, line)
	f.targets = append(f.targets, target.ID)
	return f.err
}

func alwaysRunning(*session.Instance) bool { return true }

func noTranscript(*session.Instance) string { return "" }

func TestBuild_TextNamesSourceAndHowToRead(t *testing.T) {
	a := inst("a1b2c3d4-1700000000", "auth fix", "/work/auth", session.StatusIdle)
	b := inst("b9-1700000001", "review", "/work/rev", session.StatusWaiting)
	text, line := sessionref.Build(sessionref.Endpoint{Inst: a, Profile: "work"}, b, "/home/u/.claude/projects/x/abc.jsonl")
	wantText := `[ref] session "auth fix" (a1b2c3d4-1700000000) · /work/auth · status idle
read it with: agent-deck -p work session output a1b2c3d4-1700000000 -q
transcript: /home/u/.claude/projects/x/abc.jsonl`
	if text != wantText {
		t.Fatalf("text =\n%s\nwant\n%s", text, wantText)
	}
	wantLine := `[ref] session "auth fix" (a1b2c3d4-1700000000) · /work/auth · status idle · read it with: agent-deck -p work session output a1b2c3d4-1700000000 -q · transcript: /home/u/.claude/projects/x/abc.jsonl `
	if line != wantLine {
		t.Fatalf("line =\n%q\nwant\n%q", line, wantLine)
	}
}

func TestBuild_NoTranscriptNoProfileLine(t *testing.T) {
	a := inst("id-1", "A", "/p", session.StatusRunning)
	text, line := sessionref.Build(sessionref.Endpoint{Inst: a}, nil, "")
	if strings.Contains(text, "transcript:") || strings.Contains(line, "transcript:") {
		t.Fatalf("transcript named although unknown: %q", text)
	}
	if strings.Contains(text, " -p ") {
		t.Fatalf("profile flag without a profile: %q", text)
	}
	if got := strings.Count(text, "\n"); got != 1 {
		t.Fatalf("want 2 lines, got %d: %q", got+1, text)
	}
	if !strings.Contains(text, "read it with: agent-deck session output id-1 -q") {
		t.Fatalf("missing read pointer: %q", text)
	}
}

// Control characters in a title/path would be typed into B's terminal: a
// newline submits, ESC starts a sequence. They become spaces.
func TestBuild_ControlCharactersNeverReachTheLine(t *testing.T) {
	a := inst("id-2", "evil\ntitle\x1b[2J\r", "/p\nq", session.StatusIdle)
	_, line := sessionref.Build(sessionref.Endpoint{Inst: a}, nil, "/t\r\n.jsonl")
	for _, bad := range []string{"\n", "\r", "\x1b"} {
		if strings.Contains(line, bad) {
			t.Fatalf("line carries %q: %q", bad, line)
		}
	}
	if !strings.Contains(line, `"evil title [2J"`) {
		t.Fatalf("title not cleaned as expected: %q", line)
	}
}

func TestBuild_NeverCarriesOutput(t *testing.T) {
	a := inst("id-3", "A", "/p", session.StatusIdle)
	text, _ := sessionref.Build(sessionref.Endpoint{Inst: a}, nil, "")
	if len(text) > 300 {
		t.Fatalf("reference is not short (%d bytes): %q", len(text), text)
	}
}

func TestHand_InsertsLineWithoutEnter(t *testing.T) {
	a := inst("id-a", "A", "/pa", session.StatusIdle)
	b := inst("id-b", "B", "/pb", session.StatusWaiting)
	f := &fakeInsert{}
	res, err := sessionref.Hand(sessionref.Endpoint{Inst: a}, sessionref.Endpoint{Inst: b},
		sessionref.Options{Insert: f.fn, Running: alwaysRunning, Transcript: noTranscript})
	if err != nil {
		t.Fatalf("Hand: %v", err)
	}
	if len(f.calls) != 1 || f.targets[0] != "id-b" {
		t.Fatalf("want one insert into B, got %v into %v", f.calls, f.targets)
	}
	if f.calls[0] != res.Line {
		t.Fatalf("inserted %q, want Result.Line %q", f.calls[0], res.Line)
	}
	if strings.ContainsAny(f.calls[0], "\r\n") {
		t.Fatalf("inserted text carries a line break (would submit): %q", f.calls[0])
	}
	if !res.Inserted || res.Source.ID != "id-a" || res.Target.ID != "id-b" || res.Source.Status != "idle" {
		t.Fatalf("result = %+v", res)
	}
}

func TestHand_PrintOnlyInsertsNothingAndTargetNeedNotRun(t *testing.T) {
	a := inst("id-a", "A", "/pa", session.StatusIdle)
	b := inst("id-b", "B", "/pb", session.StatusStopped)
	f := &fakeInsert{}
	res, err := sessionref.Hand(sessionref.Endpoint{Inst: a}, sessionref.Endpoint{Inst: b},
		sessionref.Options{PrintOnly: true, Insert: f.fn,
			Running:    func(*session.Instance) bool { return false },
			Transcript: noTranscript})
	if err != nil {
		t.Fatalf("Hand --print: %v", err)
	}
	if len(f.calls) != 0 || res.Inserted {
		t.Fatalf("print-only inserted: %v %+v", f.calls, res)
	}
	if res.Text == "" || res.Line == "" {
		t.Fatalf("empty text: %+v", res)
	}
}

func TestHand_Refusals(t *testing.T) {
	a := inst("id-a", "A", "/pa", session.StatusIdle)
	b := inst("id-b", "B", "/pb", session.StatusIdle)
	aCopy := inst("id-a", "A again", "/pa", session.StatusIdle)
	remoteA := inst("id-ra", "RA", "/pa", session.StatusIdle)
	remoteA.SSHHost = "box"
	remoteB := inst("id-rb", "RB", "/pb", session.StatusIdle)
	remoteB.SSHHost = "box"
	up := func(*session.Instance) bool { return true }
	down := func(*session.Instance) bool { return false }
	cases := []struct {
		name     string
		src, dst sessionref.Endpoint
		running  func(*session.Instance) bool
		code     string
	}{
		{"nil source", sessionref.Endpoint{}, sessionref.Endpoint{Inst: b}, up, sessionref.CodeMissingSession},
		{"same pointer", sessionref.Endpoint{Inst: a}, sessionref.Endpoint{Inst: a}, up, sessionref.CodeSameSession},
		{"same id", sessionref.Endpoint{Inst: a}, sessionref.Endpoint{Inst: aCopy}, up, sessionref.CodeSameSession},
		{"cross profile", sessionref.Endpoint{Inst: a, Profile: "work"}, sessionref.Endpoint{Inst: b, Profile: "home"}, up, sessionref.CodeCrossProfile},
		{"source remote", sessionref.Endpoint{Inst: remoteA}, sessionref.Endpoint{Inst: b}, up, sessionref.CodeSourceRemote},
		{"target remote", sessionref.Endpoint{Inst: a}, sessionref.Endpoint{Inst: remoteB}, up, sessionref.CodeTargetRemote},
		{"target not running", sessionref.Endpoint{Inst: a}, sessionref.Endpoint{Inst: b}, down, sessionref.CodeTargetNotRunning},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeInsert{}
			res, err := sessionref.Hand(tc.src, tc.dst, sessionref.Options{Insert: f.fn, Running: tc.running, Transcript: noTranscript})
			var r *sessionref.Refusal
			if !errors.As(err, &r) || r.Code != tc.code {
				t.Fatalf("err = %v, want refusal %s", err, tc.code)
			}
			if r.Message == "" {
				t.Fatal("refusal without a message")
			}
			if res != nil || len(f.calls) != 0 {
				t.Fatalf("refused but returned %+v / inserted %v", res, f.calls)
			}
		})
	}
}

func TestHand_DefaultAndDefaultProfileAreOneProfile(t *testing.T) {
	a := inst("id-a", "A", "/pa", session.StatusIdle)
	b := inst("id-b", "B", "/pb", session.StatusIdle)
	f := &fakeInsert{}
	_, err := sessionref.Hand(sessionref.Endpoint{Inst: a, Profile: ""}, sessionref.Endpoint{Inst: b, Profile: session.DefaultProfile},
		sessionref.Options{Insert: f.fn, Running: alwaysRunning, Transcript: noTranscript})
	if err != nil {
		t.Fatalf("\"\" and %q refused as cross-profile: %v", session.DefaultProfile, err)
	}
}

func TestHand_InsertFailureIsReported(t *testing.T) {
	a := inst("id-a", "A", "/pa", session.StatusIdle)
	b := inst("id-b", "B", "/pb", session.StatusIdle)
	f := &fakeInsert{err: errors.New("tmux gone")}
	res, err := sessionref.Hand(sessionref.Endpoint{Inst: a}, sessionref.Endpoint{Inst: b},
		sessionref.Options{Insert: f.fn, Running: alwaysRunning, Transcript: noTranscript})
	var r *sessionref.Refusal
	if !errors.As(err, &r) || r.Code != sessionref.CodeInsertFailed {
		t.Fatalf("err = %v, want insert_failed", err)
	}
	if res == nil || res.Inserted {
		t.Fatalf("result after a failed insert = %+v, want Inserted=false", res)
	}
}

// With no Insert func, Hand uses B's tmux pane; B without one is refused.
func TestHand_DefaultInsertNeedsAPane(t *testing.T) {
	a := inst("id-a", "A", "/pa", session.StatusIdle)
	b := inst("id-b", "B", "/pb", session.StatusIdle)
	_, err := sessionref.Hand(sessionref.Endpoint{Inst: a}, sessionref.Endpoint{Inst: b},
		sessionref.Options{Running: alwaysRunning, Transcript: noTranscript})
	var r *sessionref.Refusal
	if !errors.As(err, &r) || r.Code != sessionref.CodeTargetNoPane {
		t.Fatalf("err = %v, want target_no_pane", err)
	}
}

func TestCheck_NeedRunningOnlyWhenAsked(t *testing.T) {
	a := inst("id-a", "A", "/pa", session.StatusIdle)
	b := inst("id-b", "B", "/pb", session.StatusStopped)
	down := func(*session.Instance) bool { return false }
	if r := sessionref.Check(sessionref.Endpoint{Inst: a}, sessionref.Endpoint{Inst: b}, false, down); r != nil {
		t.Fatalf("Check without needRunning refused: %v", r)
	}
	if r := sessionref.Check(sessionref.Endpoint{Inst: a}, sessionref.Endpoint{Inst: b}, true, down); r == nil || r.Code != sessionref.CodeTargetNotRunning {
		t.Fatalf("Check with needRunning = %v", r)
	}
}
