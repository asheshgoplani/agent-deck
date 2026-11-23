package web

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
)

const tailWinCall = `{"type":"assistant","uuid":"aold","sessionId":"conversation-1","message":{"id":"mold","role":"assistant","content":[{"type":"tool_use","id":"toolu_old","name":"Bash","input":{"command":"ls"}}]},"timestamp":"2026-11-18T10:00:00Z"}` + "\n"
const tailWinResult = `{"type":"user","uuid":"rold","sessionId":"conversation-1","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_old","content":"BIG RESULT"}]},"timestamp":"2026-11-18T10:00:01Z"}` + "\n"

func tailWinAppend(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
}

// userPad is n user rows of ~1 KiB each.
func userPad(prefix string, n int) string {
	pad := strings.Repeat("y", 1000)
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "{\"type\":\"user\",\"uuid\":\"%s-%d\",\"sessionId\":\"conversation-1\",\"message\":{\"role\":\"user\",\"content\":[{\"type\":\"text\",\"text\":\"%s %d\"}]}}\n", prefix, i, pad, i)
	}
	return b.String()
}

// abandoned is n assistant tool_use rows whose results never arrive.
func abandoned(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "{\"type\":\"assistant\",\"uuid\":\"ab-%d\",\"sessionId\":\"conversation-1\",\"message\":{\"id\":\"mab-%d\",\"role\":\"assistant\",\"content\":[{\"type\":\"tool_use\",\"id\":\"toolu_ab_%d\",\"name\":\"Bash\",\"input\":{\"command\":\"sleep\"}}]}}\n", i, i, i)
	}
	return b.String()
}

// streamLeaks reads frames until "user turn 0" and reports whether any frame
// carried BIG RESULT.
func streamLeaks(t *testing.T, st *timelineStream) bool {
	t.Helper()
	leak := false
	for {
		_, ev := st.nextFrame(t)
		if strings.Contains(ev.Data, "BIG RESULT") {
			leak = true
		}
		if strings.Contains(ev.Data, "user turn 0") {
			return leak
		}
	}
}

// startID consumes the stream's opening id-only block.
func startID(t *testing.T, st *timelineStream) string {
	t.Helper()
	ev := st.next(t)
	if ev.ID == "" || ev.Event != "" {
		t.Fatalf("first block = %+v, want id-only", ev)
	}
	return ev.ID
}

// Issue 3219 at the web layer: HTTP tail page cursor, SSE after=end.
func TestTimelineTailWindow_AfterEndOpenCallOutsideTailWindow(t *testing.T) {
	f := newTimelineFixture(t, "claude", true)
	tailWinAppend(t, f.path, tailWinCall)
	tailWinAppend(t, f.path, userPad("q", 400))
	snap := decodeTimeline(t, f.get(t, "kinds=user&tail=1"))
	st := f.openStream(t, newStreamServer(t, f), "kinds=user&after=end", nil)
	startID(t, st)
	tailWinAppend(t, f.path, tailWinResult)
	f.appendTurns(t, 0, 1)
	streamLeak := streamLeaks(t, st)
	page := f.get(t, "kinds=user&since="+url.QueryEscape(snap.NextCursor))
	pageLeak := strings.Contains(page.Body.String(), "BIG RESULT")
	if streamLeak || pageLeak {
		t.Fatalf("leak: stream=%v since-page=%v", streamLeak, pageLeak)
	}
}

// Abandoned calls (followed by a later assistant message) can never get a
// result, so they must not crowd the live call out of the dropped-open list
// even when there are more than maxDroppedOpen of them.
func TestTimelineTailWindow_AbandonedCallsDoNotEvictLiveCall(t *testing.T) {
	f := newTimelineFixture(t, "claude", true)
	tailWinAppend(t, f.path, abandoned(100))
	tailWinAppend(t, f.path, tailWinCall)
	tailWinAppend(t, f.path, userPad("q", 400))
	snap := decodeTimeline(t, f.get(t, "kinds=user&tail=1"))
	st := f.openStream(t, newStreamServer(t, f), "kinds=user&after=end", nil)
	startID(t, st)
	tailWinAppend(t, f.path, tailWinResult)
	f.appendTurns(t, 0, 1)
	streamLeak := streamLeaks(t, st)
	page := f.get(t, "kinds=user&since="+url.QueryEscape(snap.NextCursor))
	pageLeak := strings.Contains(page.Body.String(), "BIG RESULT")
	if streamLeak || pageLeak {
		t.Fatalf("leak: stream=%v since-page=%v", streamLeak, pageLeak)
	}
}
