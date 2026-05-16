package profilefixture_test

import (
	"os"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/testutil/profilefixture"
)

func TestSetup_AppliesEnvAndConfig(t *testing.T) {
	f := profilefixture.New(t, profilefixture.Options{
		EnvProfile:      "work",
		ConfigDefault:   "personal",
		ClaudeConfigDir: "",
	})

	if got := os.Getenv("AGENTDECK_PROFILE"); got != "work" {
		t.Fatalf("AGENTDECK_PROFILE=%q want work", got)
	}
	// Env wins over config default.
	if got := session.GetEffectiveProfile(""); got != "work" {
		t.Fatalf("GetEffectiveProfile()=%q want work (env precedence)", got)
	}

	_ = f
}

func TestSetup_ClaudeConfigDirInference(t *testing.T) {
	profilefixture.New(t, profilefixture.Options{
		ClaudeConfigDir: "/home/u/.claude-work",
	})

	if got := session.GetEffectiveProfile(""); got != "work" {
		t.Fatalf("GetEffectiveProfile()=%q want work (CLAUDE_CONFIG_DIR inference)", got)
	}
}

func TestSetup_RestoresEnvOnCleanup(t *testing.T) {
	t.Setenv("AGENTDECK_PROFILE", "before")

	t.Run("inner", func(t *testing.T) {
		profilefixture.New(t, profilefixture.Options{
			EnvProfile: "inner",
		})
		if got := os.Getenv("AGENTDECK_PROFILE"); got != "inner" {
			t.Fatalf("inside child: got %q want inner", got)
		}
	})

	if got := os.Getenv("AGENTDECK_PROFILE"); got != "before" {
		t.Fatalf("after child cleanup: got %q want before", got)
	}
}

func TestProbe_RecordsAllFive(t *testing.T) {
	f := profilefixture.New(t, profilefixture.Options{
		EnvProfile: "scratch",
	})

	probes := f.Probe(profilefixture.Probes{
		CLI:        func() string { return "scratch" },
		WebAPI:     func() string { return "scratch" },
		WebProfile: func() string { return "scratch" },
		Healthz:    func() string { return "scratch" },
		TUI:        func() string { return "scratch" },
	})

	if len(probes) != 5 {
		t.Fatalf("Probe returned %d entries; want 5", len(probes))
	}
	for name, val := range probes {
		if val != "scratch" {
			t.Errorf("probe %q = %q; want scratch", name, val)
		}
	}
}

func TestAssertParity_FailsOnDivergence(t *testing.T) {
	f := profilefixture.New(t, profilefixture.Options{EnvProfile: "x"})

	stub := &stubT{}
	f.AssertParity(stub, profilefixture.Probes{
		CLI:        func() string { return "x" },
		WebAPI:     func() string { return "x" },
		WebProfile: func() string { return "DIVERGED" },
		Healthz:    func() string { return "x" },
		TUI:        func() string { return "x" },
	})
	if !stub.failed {
		t.Fatal("AssertParity should fail when probes diverge")
	}
}

func TestAssertParity_PassesOnAgreement(t *testing.T) {
	f := profilefixture.New(t, profilefixture.Options{EnvProfile: "agree"})
	f.AssertParity(t, profilefixture.Probes{
		CLI:        func() string { return "agree" },
		WebAPI:     func() string { return "agree" },
		WebProfile: func() string { return "agree" },
		Healthz:    func() string { return "agree" },
		TUI:        func() string { return "agree" },
	})
}

type stubT struct{ failed bool }

func (s *stubT) Errorf(format string, args ...any) { s.failed = true }
func (s *stubT) Helper()                           {}
