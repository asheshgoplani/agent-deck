package telemetry

import "testing"

// A CLI fork is published as its own create via, so fork volume is not mixed
// into cli_add (which counts `agent-deck add` only).
func TestSessionCreateCLIForkVia(t *testing.T) {
	c := env(t)
	grant(t, c)
	SessionCreated(SessionCreateInfo{Tool: "claude", Via: ViaCLIFork, SessionID: "s1"})
	var got []string
	for _, l := range spoolLines(t) {
		if l.E == "session.create" {
			v, _ := l.P["via"].(string)
			got = append(got, v)
		}
	}
	if len(got) != 1 || got[0] != "cli_fork" {
		t.Fatalf("session.create via = %v, want exactly [cli_fork]", got)
	}
}
