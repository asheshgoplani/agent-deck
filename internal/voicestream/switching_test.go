package voicestream

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/voicestate"
)

// The daemon writes "switching" = {title, sessionId, remote} before the
// first ssh call of a spoken switch; target stays the old session until
// it is done. The frame, `voice state --json` and the mirror carry it as
// {id, title, host} (host named like target.host); absent = null. It is a
// hint only: target is never taken from it.
func TestSwitching_CarriedThroughFrameViewAndMirror(t *testing.T) {
	dir := t.TempDir()
	body := `{"schema":"claude-code-voice.state/v1","on":true,"mode":"listening","phase":"hearing","phaseSince":1,"heartbeatMs":10000,
 "daemon":{"pid":` + itoa(int64(os.Getpid())) + `},"updatedAt":` + itoa(time.Now().UnixMilli()) + `,
 "target":{"title":"alpha","sessionId":"aaaa1111-1794950000","profile":"default","remote":null},
 "switching":{"title":"beta","sessionId":"bbbb2222-1794950000","remote":"box"}}`
	if err := os.WriteFile(filepath.Join(dir, "current.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	st := voicestate.NewReader(dir).Poll()
	if st == nil {
		t.Fatal("fixture not live")
	}
	frame, _ := json.Marshal(FromState(st, "laptop", ReasonChange, time.Now()))
	if !strings.Contains(string(frame), `"switching":{"host":"box","id":"bbbb2222-1794950000","profile":"","title":"beta"}`) &&
		!strings.Contains(string(frame), `"switching":{"id":"bbbb2222-1794950000","title":"beta","host":"box","profile":""}`) {
		t.Fatalf("voice.state frame lacks switching: %s", frame)
	}
	if !strings.Contains(string(frame), `"target":{"id":"aaaa1111-1794950000"`) {
		t.Fatalf("target must stay the old session during a switch: %s", frame)
	}
	view, _ := json.Marshal(voicestate.NewView(st, time.Now()))
	if !strings.Contains(string(view), `"switching":{"id":"bbbb2222-1794950000","title":"beta","host":"box"`) {
		t.Fatalf("voice state --json lacks switching: %s", view)
	}
	mirror, _ := json.Marshal(voicestate.MirrorFor(st, "box", "laptop", 1))
	if !strings.Contains(string(mirror), `"switching":{"title":"beta","sessionId":"bbbb2222-1794950000"`) || !strings.Contains(string(mirror), `"remote":""`) {
		t.Fatalf("mirror lacks switching (named from the box: remote \"\"): %s", mirror)
	}
	// Absent = null in the frame and the view.
	plain := strings.Replace(body, `"switching":{"title":"beta","sessionId":"bbbb2222-1794950000","remote":"box"}`, `"x":0`, 1)
	if err := os.WriteFile(filepath.Join(dir, "current.json"), []byte(plain), 0o600); err != nil {
		t.Fatal(err)
	}
	st = voicestate.NewReader(dir).Poll()
	frame, _ = json.Marshal(FromState(st, "laptop", ReasonChange, time.Now()))
	view, _ = json.Marshal(voicestate.NewView(st, time.Now()))
	if !strings.Contains(string(frame), `"switching":null`) || !strings.Contains(string(view), `"switching":null`) {
		t.Fatalf("absent switching must be null: frame %s view %s", frame, view)
	}
}
