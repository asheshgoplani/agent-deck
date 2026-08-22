package ui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/agents"
)

// A Home built directly by a test (rather than through NewHome) leaves
// optional panels nil. Every entry point must tolerate that instead of
// panicking in the update or render path.
func TestAgentsPanelNilReceiverIsSafe(t *testing.T) {
	var ap *AgentsPanel

	if ap.IsVisible() {
		t.Error("nil panel reports visible")
	}
	if ap.HasAgents() {
		t.Error("nil panel reports agents")
	}
	if got := ap.View(); got != "" {
		t.Errorf("nil panel rendered %q, want empty", got)
	}
	if sel := ap.Selected(); sel != nil {
		t.Error("nil panel returned a selection")
	}
	// None of these may panic.
	ap.Show()
	ap.Hide()
	ap.SetSize(80, 24)
	ap.SetView(agents.View{}, time.Now())
	ap.SetRules(map[string][]string{"x": {"POLICY.md"}})
	if _, cmd := ap.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}}); cmd != nil {
		t.Error("nil panel returned a command")
	}
}

func TestAgentsPanelEmptyRegistryHasNoAgents(t *testing.T) {
	ap := NewAgentsPanel()
	ap.SetView(agents.View{}, time.Now())
	if ap.HasAgents() {
		t.Error("an empty view reports agents; the panel would open for a zero-config user")
	}
}

func TestAgentsPanelRendersRegistryLoadFailureAsUnknown(t *testing.T) {
	ap := NewAgentsPanel()
	ap.SetLoadError("permission denied", time.Now())
	ap.Show()
	ap.SetSize(100, 30)
	out := ap.View()
	if !strings.Contains(out, "ERROR: agents registry could not be loaded") || !strings.Contains(out, "permission denied") {
		t.Fatalf("load failure rendered as empty instead of explicit error:\n%s", out)
	}
	if strings.Contains(out, "Nothing adopted yet") {
		t.Fatalf("unknown registry rendered as zero agents:\n%s", out)
	}
}

func testView() agents.View {
	return agents.View{
		TotalAgents: 2,
		Machines: []agents.Machine{{
			Name: "g14",
			Link: agents.LinkLocal,
			Agents: []agents.AgentRow{
				{Name: "conductor", Role: "manager", RoleVersion: "0.1.0", Class: agents.ClassAgent, State: agents.RunIdle},
				{Name: "mail-watcher", Role: "triage", Class: agents.ClassAgent, State: agents.RunNeedsYou},
			},
		}},
	}
}

// The cursor must land on agents, never on a machine header.
func TestAgentsPanelCursorSkipsMachineHeaders(t *testing.T) {
	ap := NewAgentsPanel()
	ap.SetSize(120, 40)
	ap.Show()
	ap.SetView(testView(), time.Now())

	sel := ap.Selected()
	if sel == nil {
		t.Fatal("no selection after SetView; cursor is parked on a header")
	}
	if sel.Name != "conductor" {
		t.Errorf("first selection is %q, want conductor", sel.Name)
	}

	ap.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if sel = ap.Selected(); sel == nil || sel.Name != "mail-watcher" {
		t.Errorf("after j, selection is %+v, want mail-watcher", sel)
	}
	// Past the end, the cursor stays put rather than falling off.
	ap.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if sel = ap.Selected(); sel == nil || sel.Name != "mail-watcher" {
		t.Errorf("cursor moved past the last row: %+v", sel)
	}
}

// Rows are measured in display cells. The glyph carries an SGR escape, so a
// byte-length check silently lost ~20 columns at every width.
func TestAgentsPanelRowsUseDisplayWidthNotBytes(t *testing.T) {
	view := agents.View{
		TotalAgents: 1,
		Machines: []agents.Machine{{
			Name: "g14", Link: agents.LinkLocal,
			Agents: []agents.AgentRow{{
				Name: "gmail-watcher-imap-poll", Role: "connector",
				Class: agents.ClassAgent, State: agents.RunIdle,
				Triggers: []agents.TriggerRow{{
					Name: "poll", Kind: agents.TriggerCron, External: true,
					ExternalSource: "/x.plist", NextDueText: "every 5m",
				}},
			}},
		}},
	}

	for _, width := range []int{80, 100, 120} {
		ap := NewAgentsPanel()
		ap.SetSize(width, 40)
		ap.Show()
		ap.SetView(view, time.Now())

		out := ap.View()
		for _, line := range strings.Split(out, "\n") {
			if got := cellWidth(line); got > width {
				t.Errorf("width %d: rendered line is %d cells wide:\n%s", width, got, line)
			}
		}
		// At 80 columns there is room for the next-due cell; a byte-length
		// truncation dropped it while leaving the row visibly short.
		if width >= 80 && !strings.Contains(out, "every 5m") {
			t.Errorf("width %d: next-due was truncated away though the row had room:\n%s", width, out)
		}
	}
}

// The prompt names RULES as part of the detail screen.
func TestAgentsPanelDetailRendersRules(t *testing.T) {
	ap := NewAgentsPanel()
	ap.SetSize(120, 40)
	ap.Show()
	ap.SetView(testView(), time.Now())
	ap.SetRules(map[string][]string{"conductor": {"POLICY.md", "PR-POLICY.md"}})

	ap.Update(tea.KeyMsg{Type: tea.KeyEnter})
	out := ap.View()

	if !strings.Contains(out, "RULES") {
		t.Errorf("detail screen has no RULES section:\n%s", out)
	}
	if !strings.Contains(out, "PR-POLICY.md") {
		t.Errorf("detail screen does not list the role's policy files:\n%s", out)
	}
}

// A record that claims to be armed must not be painted under a blanket
// "this is disabled" footer.
func TestAgentsPanelDoesNotCallAnArmedRecordDisabled(t *testing.T) {
	view := agents.View{
		TotalAgents: 1,
		Machines: []agents.Machine{{
			Name: "g14", Link: agents.LinkLocal,
			Agents: []agents.AgentRow{{
				Name: "armed", Role: "builder", Class: agents.ClassAgent, State: agents.RunIdle,
				Triggers: []agents.TriggerRow{{
					Name: "cron", Kind: agents.TriggerCron, Enabled: true, External: false,
					NextDueText: "cron 5m",
				}},
			}},
		}},
	}
	ap := NewAgentsPanel()
	ap.SetSize(120, 40)
	ap.Show()
	ap.SetView(view, time.Now())
	ap.Update(tea.KeyMsg{Type: tea.KeyEnter})

	out := ap.View()
	if strings.Contains(out, "it does not run it") {
		t.Errorf("an armed record was rendered under the disabled footer:\n%s", out)
	}
	if !strings.Contains(out, "ARMED") {
		t.Errorf("an armed trigger is not called out:\n%s", out)
	}
}

// Definition content is data, not markup.
func TestAgentsPanelStripsControlSequencesFromContent(t *testing.T) {
	view := agents.View{
		TotalAgents: 1,
		Machines: []agents.Machine{{
			Name: "g14", Link: agents.LinkLocal,
			Agents: []agents.AgentRow{{
				Name:      "evil\x1b[2K\rforged",
				Role:      "triage",
				Class:     agents.ClassAgent,
				State:     agents.RunIdle,
				Attention: "x\x1b[31mred",
			}},
		}},
	}
	ap := NewAgentsPanel()
	ap.SetSize(120, 40)
	ap.Show()
	ap.SetView(view, time.Now())

	out := ap.View()
	if strings.Contains(out, "\x1b[2K") || strings.Contains(out, "\r") {
		t.Errorf("a control sequence from definition content reached the rendered output:\n%q", out)
	}
}

// A machine that was never contacted must not be labelled "link ok".
func TestAgentsPanelNeverContactedMachineSaysSo(t *testing.T) {
	view := agents.View{
		TotalAgents: 1,
		Machines: []agents.Machine{{
			Name: "mac-studio", Link: agents.LinkNotContacted,
			Agents: []agents.AgentRow{{Name: "x", Role: "triage", Class: agents.ClassAgent, State: agents.RunUnknown}},
		}},
	}
	ap := NewAgentsPanel()
	ap.SetSize(120, 40)
	ap.Show()
	ap.SetView(view, time.Now())

	out := ap.View()
	if strings.Contains(out, "link ok") {
		t.Errorf("an uncontacted machine claims link ok:\n%s", out)
	}
	if !strings.Contains(out, "not contacted") {
		t.Errorf("an uncontacted machine does not say so:\n%s", out)
	}
}

// Round 2 R1: the DETAIL screen's header and trigger fields are definition
// content too, and were not sanitized. An unsanitized post id could erase the
// panel border and forge the "link ok" line this feature exists to make
// trustworthy.
func TestAgentsPanelDetailStripsControlSequences(t *testing.T) {
	view := agents.View{
		TotalAgents: 1,
		Machines: []agents.Machine{{
			Name: "g14", Link: agents.LinkLocal,
			Agents: []agents.AgentRow{{
				Name:      "ok-name",
				Role:      "manager\x1b[2K\rlink ok, drained 2m ago",
				PostID:    "post\x1b[2K\rforged",
				ReportsTo: "human:root\x1b[31m",
				Machine:   "g14\x1b[2K",
				Class:     agents.ClassAgent,
				State:     agents.RunIdle,
				Triggers: []agents.TriggerRow{{
					Name: "trig\x1b[2K\rDISABLED", Kind: agents.TriggerCron,
					External: true, ExternalSource: "/x.plist",
					NextDueText: "cron 5m\x1b[2K", Note: "note\x1b[2K",
				}},
			}},
		}},
	}
	ap := NewAgentsPanel()
	ap.SetSize(120, 40)
	ap.Show()
	ap.SetView(view, time.Now())
	ap.Update(tea.KeyMsg{Type: tea.KeyEnter})

	out := ap.View()
	if strings.Contains(out, "\x1b[2K") || strings.Contains(out, "\r") {
		t.Errorf("detail screen passed a control sequence through:\n%q", out)
	}
}
