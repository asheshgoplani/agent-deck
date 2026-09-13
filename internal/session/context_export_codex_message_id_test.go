package session

import "testing"

func TestContextExportCodexMessageIDsAreNotSessionIDs(t *testing.T) {
	for _, tc := range []struct {
		name, data string
		wantErr    bool
	}{
		{"response message ID", `{"type":"session_meta","payload":{"id":"native-123"}}
{"type":"response_item","payload":{"type":"message","id":"msg-other","role":"assistant","content":[{"type":"output_text","text":"SECRET_82"}]}}
`, false},
		{"foreign session metadata", `{"type":"session_meta","payload":{"id":"foreign"}}` + "\n", true},
		{"foreign explicit session ID on message", `{"type":"response_item","payload":{"id":"msg-other","session_id":"foreign"}}` + "\n", true},
		{"legacy foreign metadata", `{"payload":{"id":"foreign"}}` + "\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateJSONLIdentity([]byte(tc.data), "native-123")
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, tc.wantErr)
			}
		})
	}
}
