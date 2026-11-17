package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVoiceDoUsageErrors(t *testing.T) {
	for _, args := range [][]string{
		{"do"},
		{"do", "--json"},
		{"do", "--from", "radio", "hello"},
		{"do", "--answer-delay-ms", "soon", "yes"},
		{"do", "--pending"},
		{"do", "--bogus", "hello"},
	} {
		code, out, errs := runVoiceT(t, args...)
		if code != 2 || out != "" || !strings.Contains(errs, "voice do") {
			t.Errorf("args %v: code=%d out=%q err=%q", args, code, out, errs)
		}
	}
}

func TestVoiceDoIsListedInHelp(t *testing.T) {
	code, out, _ := runVoiceT(t, "help")
	if code != 0 || !strings.Contains(out, "agent-deck voice do") {
		t.Fatalf("help: code=%d out=%q", code, out)
	}
}

// "Not mine" is decided from the words alone: no config, no state, no
// session listing, nothing written under HOME.
func TestVoiceDoNotMineTouchesNothing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	for _, s := range []string{"hello there", "switch to deck conductor", "what is this", "ye kya hai", "stop"} {
		code, out, errs := runVoiceT(t, "do", "--json", "--from", "mac", s)
		if code != 0 || errs != "" {
			t.Fatalf("%q: code=%d err=%q", s, code, errs)
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(out), &m); err != nil {
			t.Fatalf("%q: not one JSON object: %q", s, out)
		}
		if m["handled"] != false || m["intent"] != "none" || m["say"] != "" || m["confirm"] != nil || m["then"] != nil {
			t.Errorf("%q: %v", s, m)
		}
		for _, k := range []string{"handled", "intent", "lang", "target", "say", "show", "confirm", "then", "refused", "dryRun"} {
			if _, ok := m[k]; !ok {
				t.Errorf("%q: missing key %s", s, k)
			}
		}
		// text mode prints nothing for a sentence that is not the desk's
		code, out, _ = runVoiceT(t, "do", s)
		if code != 0 || out != "" {
			t.Errorf("%q text mode: code=%d out=%q", s, code, out)
		}
	}
	ents, _ := os.ReadDir(home)
	if len(ents) != 0 {
		t.Errorf("not-mine path wrote under HOME: %v", ents)
	}
}

func TestParseVoiceDoArgs(t *testing.T) {
	a, err := parseVoiceDoArgs([]string{"--json", "--dry-run", "--from=glasses", "--on", "abc", "--pending", "f00", "--answer-delay-ms", "1200", "tell", "x", "to", "go"})
	if err != nil {
		t.Fatal(err)
	}
	if !a.jsonOut || !a.dryRun || a.from != "glasses" || a.on != "abc" || a.pending != "f00" || !a.hasDelay || a.delayMs != 1200 || a.sentence != "tell x to go" {
		t.Errorf("parsed %+v", a)
	}
	b, err := parseVoiceDoArgs([]string{"--json", "--", "--stop", "now"})
	if err != nil || b.sentence != "--stop now" {
		t.Errorf("after --: %+v %v", b, err)
	}
}

// "help" or a leading minus inside the sentence is a sentence, not a flag.
func TestVoiceDoHelpWordIsASentence(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, args := range [][]string{{"do", "--json", "help"}, {"do", "--json", "-5", "degrees"}} {
		code, out, errs := runVoiceT(t, args...)
		var m map[string]any
		if code != 0 || json.Unmarshal([]byte(out), &m) != nil || m["handled"] != false {
			t.Errorf("args %v: code=%d out=%q err=%q", args, code, out, errs)
		}
	}
}

// --on absent is "on no session": the desk never takes the mic's session
// from the voice state, so a tell asks first even while the voice state
// names that session.
func TestVoiceDoOnAbsentIsNone(t *testing.T) {
	a, err := parseVoiceDoArgs([]string{"--json", "tell", "x", "to", "go"})
	if err != nil || a.on != "" {
		t.Fatalf("parsed %+v %v", a, err)
	}
	src, err := os.ReadFile("voice_do_cmd.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(src), "voicestate.") {
		t.Fatal("voice do reads the voice state to guess --on")
	}
}

// voice state: a daemon that does not say where the mic is gives source "".
func TestVoiceStateSourceUnknownIsEmpty(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_VOICE_STATE_DIR", dir)
	code, out, _ := runVoiceT(t, "state", "--json")
	var m map[string]any
	if code != 0 || json.Unmarshal([]byte(out), &m) != nil || m["source"] != "" {
		t.Fatalf("code=%d out=%q", code, out)
	}
}
