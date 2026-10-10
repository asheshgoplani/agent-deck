package session

import (
	"strings"
	"testing"
)

// Prime-agent "no reply to no-op notices"): the conductor's shared
// instructions and every launched child's identity block tell the agent not
// to answer a notice that needs no action. Literal strings on purpose: the
// wording is what the agent reads.

func TestNoOpNoticeRule_InSharedConductorTemplate(t *testing.T) {
	for _, agent := range []string{ConductorAgentClaude, ConductorAgentCodex, ConductorAgentHermes} {
		spec, err := GetConductorAgentSpec(agent)
		if err != nil {
			t.Fatal(err)
		}
		got := renderConductorInstructionsTemplate(conductorSharedClaudeMDTemplate, "", DefaultProfile, spec)
		for _, want := range []string{
			"**No-op notices get no reply.**",
			"a pure acknowledgement such as \"ok\", \"thanks\" or \"noted\", or a status ping that changes nothing",
			"do not answer it, do not send anything back to the child, just end the turn.",
			"A `[HEARTBEAT]` is not a no-op notice; answer it in the format below.",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("%s shared conductor instructions missing %q", agent, want)
			}
		}
		// The rule sits with the inbox record guidance, before the heartbeat
		// response format it points at.
		rule, format := strings.Index(got, "**No-op notices get no reply.**"), strings.Index(got, "**Your heartbeat response format:**")
		if rule < 0 || format < 0 || rule > format {
			t.Errorf("%s: no-op rule at %d must come before the heartbeat format at %d", agent, rule, format)
		}
	}
}

func TestNoOpNoticeRule_InChildIdentityBlock(t *testing.T) {
	identityTestEnv(t)
	for _, tool := range []string{"claude", "codex"} {
		got := identityTestInstance(tool).BuildIdentityPrompt()
		want := "- A message or record that needs nothing from you (a pure acknowledgement such as \"ok\" or \"thanks\", or a status ping that changes nothing) is a no-op notice: do not reply to it, end the turn.\n"
		if !strings.Contains(got, want) {
			t.Errorf("%s identity block missing the no-op notice rule:\n%s", tool, got)
		}
	}
}
