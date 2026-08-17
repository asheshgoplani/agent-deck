package session

import (
	"errors"
	"fmt"
	"strings"
)

// SyncGroupCodexHomeSkills materializes a group's declared Codex home skills
// into its resolved CODEX_HOME/skills directory.
//
// Home skills are otherwise attached only by ApplyConfiguredLoadout at session
// create/start, so a freshly declared skill stays absent until someone happens
// to start a session in that group — invisible unless `config doctor` is run.
// This gives `group codex sync` the same reach for skills that it already has
// for marketplaces and plugins, so one command converges the whole home.
//
// Semantics match ApplyConfiguredLoadout exactly, because a session start must
// not undo or contradict what this wrote:
//
//   - already attached and healthy -> silent no-op
//   - target present but not a healthy managed attachment -> skipped, reported;
//     a human-placed directory always beats config
//   - entry not resolvable in the skill-source registry -> skipped, reported
//   - removing an entry from config.toml does NOT detach it here either
//
// Returns the per-entry problems as strings rather than failing the sync: one
// unresolvable skill must not block the rest of the home from converging.
func SyncGroupCodexHomeSkills(groupPath string) ([]string, error) {
	codexHome, skills, err := ResolveGroupCodexHomeSkills(groupPath)
	if err != nil {
		return nil, err
	}
	if len(skills) == 0 {
		return nil, nil
	}
	if codexHome == "" {
		// ResolveGroupCodexHomeSkills already distinguishes "only the global
		// floor applies and there is no home" (skills empty) from a group that
		// asked for skills without a config_dir (error), so reaching here with
		// entries and no home would be a contract change upstream.
		return nil, fmt.Errorf("group %q has Codex skills but no config_dir", groupPath)
	}

	var problems []string
	for _, entry := range skills {
		_, attachErr := AttachSkillToCodexHome(codexHome, entry, "")
		switch {
		case attachErr == nil:
			// Newly attached.
		case errors.Is(attachErr, ErrSkillAlreadyAttached) && healthyManagedCodexHomeSkillAttachment(codexHome, entry):
			// Healthy managed floor — nothing to do.
		case errors.Is(attachErr, ErrSkillAlreadyAttached):
			problems = append(problems, fmt.Sprintf("skill %q: existing target is not a healthy manifest-managed attachment", entry))
		case errors.Is(attachErr, ErrSkillNotFound) || errors.Is(attachErr, ErrSkillSourceNotFound):
			problems = append(problems, fmt.Sprintf("skill %q: not found in the skill-source registry (register the store with `agent-deck skill source add`)", entry))
		case errors.Is(attachErr, ErrSkillAmbiguous):
			problems = append(problems, fmt.Sprintf("skill %q: ambiguous — qualify as <source>/<name>: %v", entry, attachErr))
		case errors.Is(attachErr, ErrSkillUnsupportedKind):
			problems = append(problems, fmt.Sprintf("skill %q: not an attachable directory skill: %v", entry, attachErr))
		default:
			problems = append(problems, fmt.Sprintf("skill %q: %v", entry, sanitizeSyncProblem(attachErr.Error())))
		}
	}
	return problems, nil
}

// sanitizeSyncProblem strips newlines so a filesystem error cannot break the
// single-line-per-problem output contract the CLI prints.
func sanitizeSyncProblem(value string) string {
	value = strings.ReplaceAll(value, "\r", " ")
	return strings.ReplaceAll(value, "\n", " ")
}
