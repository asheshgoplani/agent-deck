package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/needsme"
	"github.com/asheshgoplani/agent-deck/internal/voicestate"
)

func runVoiceT(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := runVoice("default", args, &out, &errb)
	return code, out.String(), errb.String()
}

func TestVoiceDispatcherUsage(t *testing.T) {
	for _, args := range [][]string{nil, {"bogus"}, {"state", "--nope"}} {
		code, out, errs := runVoiceT(t, args...)
		if code != 2 || out != "" || !strings.Contains(errs, "voice <subcommand>") {
			t.Errorf("args %v: code=%d out=%q err=%q", args, code, out, errs)
		}
	}
	code, out, _ := runVoiceT(t, "help")
	if code != 0 || !strings.Contains(out, "state") || !strings.Contains(out, "voice state --json") {
		t.Errorf("help: code=%d out=%q", code, out)
	}
}

func TestVoiceStateNoVoice(t *testing.T) {
	t.Setenv("CLAUDE_VOICE_STATE_DIR", t.TempDir())
	code, out, errs := runVoiceT(t, "state", "--json")
	if code != 0 || errs != "" {
		t.Fatalf("code=%d err=%q", code, errs)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("not json: %q", out)
	}
	if m["on"] != false || m["phase"] != "off" || m["from"] != "none" || m["target"] != nil {
		t.Errorf("state = %v", m)
	}
	for _, k := range []string{"on", "phase", "phaseSince", "muted", "target", "source", "from", "updatedAt"} {
		if _, ok := m[k]; !ok {
			t.Errorf("missing key %s", k)
		}
	}
}

func TestVoiceStateLive(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_VOICE_STATE_DIR", dir)
	now := time.Now().UnixMilli()
	body := `{"schema":"claude-code-voice.state/v1","on":true,"phase":"transcribing","phaseSince":` + strconv.FormatInt(now, 10) +
		`,"muted":false,"heartbeatMs":10000,"daemon":{"pid":` + strconv.Itoa(os.Getpid()) + `},"updatedAt":` + strconv.FormatInt(now, 10) +
		`,"target":{"title":"api","sessionId":"abc123","profile":"default","remote":null}}`
	if err := os.WriteFile(filepath.Join(dir, "current.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, _ := runVoiceT(t, "state", "--json")
	var m struct {
		On     bool
		Phase  string
		From   string
		Target *struct{ ID, Title, Host, Profile string }
	}
	if err := json.Unmarshal([]byte(out), &m); code != 0 || err != nil {
		t.Fatalf("code=%d err=%v out=%q", code, err, out)
	}
	if !m.On || m.Phase != "hearing" || m.From != "local" || m.Target == nil || m.Target.ID != "abc123" || m.Target.Title != "api" {
		t.Errorf("state = %+v out=%s", m, out)
	}
	// plain mode: one human line
	_, plain, _ := runVoiceT(t, "state")
	if !strings.Contains(plain, "hearing") || !strings.Contains(plain, "api") {
		t.Errorf("plain = %q", plain)
	}
}

func TestWriteNeedsMe(t *testing.T) {
	res := needsme.Result{Count: 1, Sessions: []needsme.Session{{Title: "alpha", Host: "box1", Reason: "asks a question", WaitingSeconds: 130}},
		Errors: []needsme.HostError{{Host: "box1", Error: "timeout"}, {Profile: "work", Error: "failed"}}}
	var out, errb bytes.Buffer
	writeNeedsMe(&out, &errb, res, true)
	var back needsme.Result
	if err := json.Unmarshal(out.Bytes(), &back); err != nil || back.Count != 1 || len(back.Errors) != 2 || errb.Len() != 0 {
		t.Fatalf("json: %v %q %q", err, out.String(), errb.String())
	}
	out.Reset()
	writeNeedsMe(&out, &errb, res, false)
	if out.String() != "alpha [box1]: asks a question, waiting 2m\n" || !strings.Contains(errb.String(), "box1: timeout") || !strings.Contains(errb.String(), "this machine (profile work): failed") {
		t.Errorf("text: %q / %q", out.String(), errb.String())
	}
	out.Reset()
	writeNeedsMe(&out, &errb, needsme.Result{}, false)
	if out.String() != "Nobody is waiting for you.\n" {
		t.Errorf("empty text = %q", out.String())
	}
}

func TestNeedsMeFlags(t *testing.T) {
	var out, errb bytes.Buffer
	if code := runNeedsMe("default", []string{"--bogus"}, &out, &errb, nil); code != 2 {
		t.Errorf("bad flag code = %d", code)
	}
	out.Reset()
	code := runNeedsMe("default", []string{"--json"}, &out, &errb, func() []needsme.Source { return nil })
	if code != 0 || !strings.Contains(out.String(), `"count":0`) || !strings.Contains(out.String(), `"sessions":[]`) {
		t.Errorf("empty json: code=%d %q", code, out.String())
	}
}

func TestVoiceAndNeedsMeAreRegistered(t *testing.T) {
	for _, c := range []string{"voice", "needs-me"} {
		if !commandRegistry[c] {
			t.Errorf("%s missing from commandRegistry", c)
		}
		if _, rest := extractProfileFlag([]string{c, "-p", "x"}); len(rest) != 3 {
			t.Errorf("%s: -p after the command was swallowed: %v", c, rest)
		}
	}
	code, _, _ := runVoiceT(t, "state", "-p", "nosuchprofile", "--json")
	if code != 2 {
		t.Errorf("voice state -p x must be a usage error, code = %d", code)
	}
}

func TestVoiceStateLineStripsControlCharacters(t *testing.T) {
	v := voicestate.View{On: true, Phase: "listening", Target: &voicestate.ViewTarget{Title: "al\x1bpha\nx\a", Host: "bo\x1bx"}}
	got := voiceStateLine(v)
	if strings.ContainsAny(got, "\x1b\a\n") {
		t.Errorf("control characters in %q", got)
	}
	if !strings.Contains(got, "alpha") {
		t.Errorf("title lost: %q", got)
	}
}

func TestVoiceStateLineOff(t *testing.T) {
	if got := voiceStateLine(voicestate.View{Phase: "off"}); got != "voice: off" {
		t.Errorf("got %q", got)
	}
	if got := voiceStateLine(voicestate.View{On: true, Phase: "resting", Extra: "call"}); got != "voice: resting (call)" {
		t.Errorf("got %q", got)
	}
}

func TestNeedsMeHelpAndTimeoutRange(t *testing.T) {
	var out, errb bytes.Buffer
	none := func() []needsme.Source { return nil }
	if code := runNeedsMe("default", []string{"--help"}, &out, &errb, none); code != 0 {
		t.Errorf("--help code = %d", code)
	}
	for _, bad := range []string{"0", "-1s", "10s"} {
		errb.Reset()
		if code := runNeedsMe("default", []string{"--timeout", bad}, &out, &errb, none); code != 2 || !strings.Contains(errb.String(), "timeout") {
			t.Errorf("--timeout %s: code=%d err=%q", bad, code, errb.String())
		}
	}
	if code := runNeedsMe("default", []string{"--timeout", "2s"}, &out, &errb, none); code != 0 {
		t.Errorf("--timeout 2s code = %d", code)
	}
	if code, _, _ := runVoiceT(t, "state", "--help"); code != 0 {
		t.Errorf("voice state --help code = %d", code)
	}
}
