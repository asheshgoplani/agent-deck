package session

// Configured-conductor setup helpers.
//
// "Configured mode" (agent-deck conductor setup <name> --configured --role …)
// collapses the multi-step ritual of standing up a loadout+identity conductor
// (the harness-advisor / lilu shape) into the setup command:
//
//  1. Write the [conductors.<name>.claude] stanza into config.toml so the
//     conductor spawns with AGENT_ROLE (+ optional model/effort) and a
//     declarative skill loadout. (WriteConductorClaudeConfig)
//  2. Materialize that loadout via the existing ApplyConfiguredLoadout entry
//     point (driven from the CLI, not here — this file only owns config).
//  3. Skip re-creating the retired shared base CLAUDE.md, which auto-loads
//     into every conductor. (MaybeInstallSharedConductorInstructions)
//
// Env keys mirror the established live format: model/effort are expressed as
// ANTHROPIC_MODEL / CLAUDE_CODE_EFFORT_LEVEL inside the env map (not the
// separate [conductors.X.claude].model field), so a configured conductor's
// stanza is byte-identical to the hand-authored harness-advisor / lilu blocks.

const (
	envKeyAgentRole   = "AGENT_ROLE"
	envKeyModel       = "ANTHROPIC_MODEL"
	envKeyEffortLevel = "CLAUDE_CODE_EFFORT_LEVEL"
)

// ConductorClaudeConfigInput carries the configured-mode inputs that map into a
// conductor's [conductors.<name>.claude] stanza.
type ConductorClaudeConfigInput struct {
	Role    string   // -> env[AGENT_ROLE]            (required in configured mode)
	Model   string   // -> env[ANTHROPIC_MODEL]       (optional)
	Effort  string   // -> env[CLAUDE_CODE_EFFORT_LEVEL] (optional)
	Plugins []string // -> [conductors.<name>.claude].plugins (declarative loadout floor)
}

// applyConductorClaudeConfig merges the configured-mode inputs into the
// conductor's [conductors.<name>.claude] block IN PLACE. It is:
//
//   - idempotent — same inputs applied twice yield identical config; managed
//     keys are set, not appended, so no duplication accumulates;
//   - no-clobber — other conductors, other top-level sections, and any
//     user-added env keys on THIS conductor are preserved. Optional inputs
//     left empty are not written (so a re-run without --model keeps a
//     previously configured ANTHROPIC_MODEL rather than deleting it).
//
// Pure (no IO) so it is unit-testable directly on an in-memory *UserConfig.
func applyConductorClaudeConfig(config *UserConfig, name string, in ConductorClaudeConfigInput) {
	if config == nil || name == "" {
		return
	}
	if config.Conductors == nil {
		config.Conductors = map[string]ConductorOverrides{}
	}

	// Map values are copies; mutate a local and reassign.
	ov := config.Conductors[name]
	if ov.Claude.Env == nil {
		ov.Claude.Env = map[string]string{}
	}
	if in.Role != "" {
		ov.Claude.Env[envKeyAgentRole] = in.Role
	}
	if in.Model != "" {
		ov.Claude.Env[envKeyModel] = in.Model
	}
	if in.Effort != "" {
		ov.Claude.Env[envKeyEffortLevel] = in.Effort
	}
	if len(in.Plugins) > 0 {
		ov.Claude.Plugins = append([]string(nil), in.Plugins...)
	}
	config.Conductors[name] = ov
}

// WriteConductorClaudeConfig loads the live config, applies the configured-mode
// stanza for `name`, and saves it back. Idempotency and no-clobber come for
// free from the full load → round-trip → save path (SaveUserConfig re-encodes
// the entire in-memory config, preserving every unrelated section).
func WriteConductorClaudeConfig(name string, in ConductorClaudeConfigInput) error {
	config, err := LoadUserConfig()
	if err != nil {
		return err
	}
	if config == nil {
		config = &UserConfig{}
	}
	applyConductorClaudeConfig(config, name, in)
	return SaveUserConfig(config)
}

// MaybeInstallSharedConductorInstructions installs the shared base instructions
// file (the conductor-dir CLAUDE.md / AGENTS.md that auto-loads into every
// conductor session) UNLESS skip is true. Configured-override conductors manage
// their own per-conductor instructions; re-creating the retired stock shared
// base silently re-introduces a CLAUDE.md that loads into every conductor — the
// friction observed 2026-06-15. Returns whether the install ran.
func MaybeInstallSharedConductorInstructions(agent, customPath string, skip bool) (installed bool, err error) {
	if skip {
		return false, nil
	}
	return true, InstallSharedConductorInstructions(agent, customPath)
}
