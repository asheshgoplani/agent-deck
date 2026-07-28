package cchook

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type Level int

const (
	LevelUser Level = iota
	LevelProject
	LevelLocal
	LevelManaged
)

func (l Level) String() string {
	switch l {
	case LevelUser:
		return "user"
	case LevelProject:
		return "project"
	case LevelLocal:
		return "local"
	case LevelManaged:
		return "managed"
	default:
		return "unknown"
	}
}

type HookEntry struct {
	Command string
	Level   Level
}

type ResolvedHooks struct {
	Entries []HookEntry
}

type settingsFile struct {
	Hooks map[string][]hookGroup `json:"hooks"`
}

type hookGroup struct {
	Hooks []hookDef `json:"hooks"`
}

type hookDef struct {
	Type    string `json:"type"`
	Command string `json:"command"`
}

// ResolveWorktreeHooks reads CC settings from all 4 levels and extracts hook
// entries for the given event. Returns nil if no hooks are configured.
// The Entries slice is ordered by priority: user > project > local > managed.
// This ordering was validated experimentally against Claude Code 2.1.x by
// configuring WorktreeCreate hooks at all 4 levels, each logging invocation
// and returning a distinct path. All hooks fired in parallel (confirmed via
// logs), but "claude --worktree" used the user-level path. Modulating timing
// (sleep in select hooks) and selective removal confirmed the precedence is
// deterministic and independent of completion order.
func ResolveWorktreeHooks(event string, repoDir string, userClaudeDir string, managedDir string) *ResolvedHooks {
	type source struct {
		dir   string
		file  string
		level Level
	}

	sources := []source{
		{userClaudeDir, "settings.json", LevelUser},
		{repoDir, filepath.Join(".claude", "settings.json"), LevelProject},
		{repoDir, filepath.Join(".claude", "settings.local.json"), LevelLocal},
		{managedDir, "managed-settings.json", LevelManaged},
	}

	var entries []HookEntry
	for _, src := range sources {
		if src.dir == "" {
			continue
		}
		cmds := readHookCommands(filepath.Join(src.dir, src.file), event)
		for _, cmd := range cmds {
			entries = append(entries, HookEntry{Command: cmd, Level: src.level})
		}
	}

	if len(entries) == 0 {
		return nil
	}
	return &ResolvedHooks{Entries: entries}
}

func readHookCommands(path string, event string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	var settings settingsFile
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil
	}

	groups, ok := settings.Hooks[event]
	if !ok {
		return nil
	}

	var commands []string
	for _, group := range groups {
		for _, hook := range group.Hooks {
			if hook.Type == "command" && hook.Command != "" {
				commands = append(commands, hook.Command)
			}
		}
	}
	return commands
}
