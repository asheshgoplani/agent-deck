package voicestream

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

const deskFind = `{"handled":true,"intent":"find","lang":"en",
 "target":{"id":"b2","title":"beta","host":"box"},
 "say":"One: beta. Switch to it?",
 "show":{"kind":"sessions","items":[{"id":"b2","title":"beta","host":"box","status":"waiting","line":"asks a question"}]},
 "confirm":{"token":"tok-SECRET-123","kind":"yes","prompt":"Switch to beta?","expiresMs":10000},
 "then":{"action":"switch","target":{"id":"b2","title":"beta","host":"box"}},
 "refused":null,"dryRun":false}`

func TestResultFromDesk(t *testing.T) {
	r, err := ResultFromDesk([]byte(deskFind), "glasses")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(r)
	for _, leak := range []string{"Switch to it?", "tok-SECRET-123", `"say"`, `"token"`} {
		if strings.Contains(string(raw), leak) {
			t.Fatalf("frame carries %q: %s", leak, raw)
		}
	}
	if r.Then == nil || r.Then.Action != "switch" || r.Then.Target == nil || r.Then.Target.ID != "b2" || r.Then.Target.Host != "box" {
		t.Fatalf("then = %+v", r.Then)
	}
	if r.Confirm == nil || r.Confirm.Prompt != "Switch to beta?" || r.Confirm.ExpiresMs != 10000 {
		t.Fatalf("confirm = %+v", r.Confirm)
	}
	if r.From != "glasses" || r.Intent != "find" || len(r.Show.Items) != 1 || r.SessionID() != "" {
		t.Fatalf("result = %+v sid %q", r, r.SessionID())
	}
	for _, in := range []string{`{"handled":false,"intent":"none","say":""}`, strings.Replace(deskFind, `"dryRun":false`, `"dryRun":true`, 1)} {
		if _, err := ResultFromDesk([]byte(in), "mac"); !errors.Is(err, ErrNotPublishable) {
			t.Errorf("%s: err %v, want ErrNotPublishable", in[:30], err)
		}
	}
	if _, err := ResultFromDesk([]byte(`not json`), "mac"); err == nil {
		t.Error("bad JSON accepted")
	}
	r, _ = ResultFromDesk([]byte(`{"handled":true,"intent":"status","then":null,"target":{"id":"a1","title":"alpha","host":""}}`), "mac")
	if r.Then != nil || r.SessionID() != "a1" {
		t.Errorf("no then: %+v sid %q", r.Then, r.SessionID())
	}
}
