package statedb

import (
	"encoding/json"
	"testing"
)

func TestKimiToolDataIdentityIsSticky(t *testing.T) {
	old := json.RawMessage(`{"kimi_session_id":"native-id","kimi_detected_at":123,"unrelated":true}`)
	merged := MergeToolDataExtras(old, json.RawMessage(`{"status":"running"}`))
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(merged, &fields); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"kimi_session_id", "kimi_detected_at", "unrelated"} {
		if _, ok := fields[key]; !ok {
			t.Errorf("merged tool data omitted %q: %s", key, merged)
		}
	}

	cleared := MergeToolDataExtras(old, json.RawMessage(`{"kimi_session_id":"","kimi_detected_at":0}`))
	if err := json.Unmarshal(cleared, &fields); err != nil {
		t.Fatal(err)
	}
	if string(fields["kimi_session_id"]) != `""` || string(fields["kimi_detected_at"]) != "0" {
		t.Fatalf("intentional clear was not preserved: %s", cleared)
	}
}
