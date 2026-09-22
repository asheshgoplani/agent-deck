package core

import (
	"encoding/json"
	"testing"
)

func TestEnvelopeSuccessShape(t *testing.T) {
	res := &Result{ID: "session.stop", Out: SessionStopOut{ID: "abc", Title: "t"}, Warnings: []string{"w"}}
	got, err := json.Marshal(res.Envelope("req-1"))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"schema":"agent-deck/session.stop/v1","request_id":"req-1","ok":true,"data":{"id":"abc","title":"t"},"warnings":["w"],"revision":null}`
	if string(got) != want {
		t.Fatalf("envelope\n got %s\nwant %s", got, want)
	}
}

func TestEnvelopeErrorShape(t *testing.T) {
	res := &Result{ID: "session.start", Err: Errorf(CodeNotFound, "session 'x' not found")}
	got, err := json.Marshal(res.Envelope("req-2"))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"schema":"agent-deck/session.start/v1","request_id":"req-2","ok":false,"data":null,"warnings":[],"revision":null,"error":{"code":"NOT_FOUND","message":"session 'x' not found"}}`
	if string(got) != want {
		t.Fatalf("envelope\n got %s\nwant %s", got, want)
	}
}

func TestEnvelopeGeneratesRequestID(t *testing.T) {
	a := (&Result{ID: "x"}).Envelope("")
	b := (&Result{ID: "x"}).Envelope("")
	if len(a.RequestID) != 16 || a.RequestID == b.RequestID {
		t.Fatalf("request ids %q %q", a.RequestID, b.RequestID)
	}
}
