package voicestream

import (
	"encoding/json"
	"strings"
	"testing"
)

// P1 (round 2): the desk's real `voice do --json` shapes (captured from the
// desk binary in a throwaway HOME; paths and ids neutral here). A tell puts
// his dictated message in show.items[].line, a read puts the reply it
// speaks there. voice.result carries only id, title, host and status per
// item and the show kind: never line, never any free text.
const deskTellShape = `{"handled":true,"intent":"tell","lang":"en","target":{"id":"aaaa1111-1794962055","title":"alpha-local","host":"","profile":"default"},"say":"Send to alpha local: wire the money to account 4471. Yes?","show":{"kind":"confirm","items":[{"id":"aaaa1111-1794962055","title":"alpha-local","host":"","status":"waiting","line":"wire the money to account 4471"}]},"confirm":{"token":"c57332cbcf93b59dffd7246a74492956","kind":"yes","prompt":"Send to alpha local: wire the money to account 4471. Yes?","expiresMs":10000},"then":null,"refused":null,"dryRun":false}`

const deskReadShape = `{"handled":true,"intent":"read","lang":"en","target":{"id":"aaaa1111-1794962055","title":"alpha-local","host":"","profile":"default"},"say":"The build passed on main, two tests are flaky.","show":{"kind":"sessions","items":[{"id":"aaaa1111-1794962055","title":"alpha-local","host":"","status":"waiting","line":"The build passed on main, two tests are flaky."}]},"confirm":null,"then":null,"refused":null,"dryRun":false}`

func TestResultFromDesk_DeskShapesCarryNoDictatedOrSpokenText(t *testing.T) {
	for name, c := range map[string]struct{ raw, secret string }{
		"tell": {deskTellShape, "wire the money"},
		"read": {deskReadShape, "two tests are flaky"},
	} {
		ev, err := ResultFromDesk([]byte(c.raw), "mac")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		out, _ := json.Marshal(ev)
		if strings.Contains(string(out), c.secret) {
			t.Errorf("%s: voice.result carries his words or the spoken reply: %s", name, out)
		}
		if strings.Contains(string(out), `"line"`) {
			t.Errorf("%s: voice.result has a line field: %s", name, out)
		}
		if len(ev.Show.Items) != 1 {
			t.Fatalf("%s: items %+v", name, ev.Show.Items)
		}
		it := ev.Show.Items[0]
		if it.ID != "aaaa1111-1794962055" || it.Title != "alpha-local" || it.Host != "" || it.Status != "waiting" {
			t.Errorf("%s: item lost what screens need: %+v", name, it)
		}
		if ev.Show.Kind == "" {
			t.Errorf("%s: show.kind lost", name)
		}
	}
}
