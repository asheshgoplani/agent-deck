package session

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/asheshgoplani/agent-deck/internal/comms"
)

// The extension takes a frame of at most 4096 bytes whose line is at most 900
// bytes once control characters are stripped, and refuses anything else for
// good. Go's encoder escapes a control character as six bytes, and nothing
// bounds a doorbell's id list, so the leg fits the frame before it rings:
// control characters stripped and the line held to 900 bytes, as the extension
// holds it, and ids dropped from the front until the frame fits. The ids only feed
// the extension's duplicate window; the records themselves ride the run start.
func TestPiWakeLeg_FitsTheFrameTheExtensionTakes(t *testing.T) {
	for name, tc := range map[string]struct {
		line string
		ids  int
	}{
		"control characters":  {line: strings.Repeat("\x01", 600) + "wake", ids: 2},
		"a long line":         {line: strings.Repeat("é", 700) + "wake", ids: 2},
		"a long id list":      {line: "[agent-deck msg] 3 results", ids: 400},
		"everything together": {line: strings.Repeat("\x1b[0m\"\\", 300), ids: 300},
	} {
		t.Run(name, func(t *testing.T) {
			w := newPiWakeFixture(t, replyWith("submitted"))
			ids := make([]string, tc.ids)
			for i := range ids {
				ids[i] = fmt.Sprintf("01J0RECORD%016d", i)
			}
			out, err := piWakeLeg(wakeOpts{foreground: true}, "inbox", w.parent, "default", tc.line, comms.NewID(time.Now()), ids)
			if err != nil || !out.Accepted {
				t.Fatalf("leg = %+v, %v; want the doorbell submitted, not refused", out, err)
			}
			f := w.ext.frame(t, 0)
			raw, _ := json.Marshal(f)
			if len(raw)+1 > 4096 {
				t.Errorf("frame is %d bytes, over the extension's 4096", len(raw)+1)
			}
			line, _ := f["line"].(string)
			if len(line) > comms.WakeLineBytes || !utf8.ValidString(line) || strings.ContainsAny(line, "\x01\x1b") {
				t.Errorf("line is %d bytes (valid UTF-8 %v), want at most %d with no control characters: %q",
					len(line), utf8.ValidString(line), comms.WakeLineBytes, line)
			}
			got, _ := f["ids"].([]any)
			if len(got) == 0 || got[len(got)-1] != ids[len(ids)-1] {
				t.Errorf("frame ids end %v, want the last id %q kept", got, ids[len(ids)-1])
			}
		})
	}
}
