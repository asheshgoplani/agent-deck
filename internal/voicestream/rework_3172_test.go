package voicestream

import (
	"encoding/json"
	"strings"
	"testing"
)

// 3172 rework F1: no free text reaches a voice.result frame, whatever show
// kind the desk sends. Only the session kinds keep a title.
func TestRework3172UnknownShowKindKeepsNoText(t *testing.T) {
	for _, kind := range []string{"todo", "notes", "Todos", "", "todos"} {
		raw := `{"handled":true,"intent":"todo_list","show":{"kind":"` + kind + `","items":[{"id":"t1","title":"CANARY pay the rent","host":"CANARYHOST","status":"open","line":"CANARY line"}]},"refused":null,"dryRun":false}`
		r, err := ResultFromDesk([]byte(raw), "phone")
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(r)
		if strings.Contains(string(b), "CANARY") {
			t.Errorf("kind %q: frame carries his text: %s", kind, b)
		}
	}
}

// F2: refused, intent and item status are words from a closed set; any
// other text is replaced, never copied.
func TestRework3172LabelsAreClosedWords(t *testing.T) {
	raw := `{"handled":true,"intent":"CANARYINTENT wire it","target":{"id":"a1","title":"alpha","host":""},` +
		`"show":{"kind":"sessions","items":[{"id":"a1","title":"alpha","status":"CANARYSTATUS wire the money"}]},` +
		`"refused":"CANARYREFUSED wire the money","dryRun":false}`
	r, err := ResultFromDesk([]byte(raw), "phone")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(r)
	if strings.Contains(string(b), "CANARY") {
		t.Errorf("frame carries free text: %s", b)
	}
	if r.Refused == nil || *r.Refused != "unsafe" {
		t.Errorf("an unknown refusal must still read as refused (unsafe): %v", r.Refused)
	}
	for _, ok := range []string{"other-person", "not-found", "ambiguous", "unsafe", "expired"} {
		raw := `{"handled":true,"intent":"stop","refused":"` + ok + `","dryRun":false}`
		r, _ := ResultFromDesk([]byte(raw), "phone")
		if r.Refused == nil || *r.Refused != ok {
			t.Errorf("refused %q changed to %v", ok, r.Refused)
		}
	}
}
