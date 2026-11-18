package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/events"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// The desk's result reaches the bus as voice.result with then, without
// the spoken line or the confirm token.
func TestVoiceResultPublish_PutsFrameOnBus(t *testing.T) {
	in := `{"handled":true,"intent":"find","say":"SPOKEN-LINE","target":{"id":"b2","title":"beta","host":"box"},
 "show":{"kind":"sessions","items":[]},"confirm":{"token":"TOKEN-XYZ","kind":"yes","prompt":"Switch to beta?","expiresMs":10000},
 "then":{"action":"switch","target":{"id":"b2","title":"beta","host":"box"}},"refused":null,"dryRun":false}`
	var out, errb bytes.Buffer
	if code := voiceResultPublish("", []string{"--from", "mac"}, strings.NewReader(in), &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), `"published":true`) {
		t.Fatalf("stdout %q", out.String())
	}
	// The publish is asynchronous (the CLI's exit drains it): wait for the
	// log to exist.
	var bus *events.Bus
	var err error
	for deadline := time.Now().Add(3 * time.Second); ; {
		if bus, err = events.OpenReader(session.GetEffectiveProfile("")); err == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer bus.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	sub, err := bus.Subscribe(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	for f := range sub.Frames() {
		if f.Kind != "voice.result" {
			continue
		}
		data := string(f.Data)
		if strings.Contains(data, "SPOKEN-LINE") || strings.Contains(data, "TOKEN-XYZ") {
			t.Fatalf("frame leaks say or token: %s", data)
		}
		if !strings.Contains(data, `"then":{"action":"switch"`) || !strings.Contains(data, `"from":"mac"`) {
			t.Fatalf("frame data %s", data)
		}
		return
	}
	t.Fatal("no voice.result frame on the bus")
}

func TestVoiceResultPublish_NotHandledPublishesNothing(t *testing.T) {
	var out, errb bytes.Buffer
	if code := voiceResultPublish("", nil, strings.NewReader(`{"handled":false,"intent":"none","say":""}`), &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), `"published":false`) {
		t.Fatalf("stdout %q", out.String())
	}
	if code := voiceResultPublish("", []string{"--from", "radio"}, strings.NewReader(`{}`), &out, &errb); code != 2 {
		t.Fatalf("bad --from: exit %d", code)
	}
}
