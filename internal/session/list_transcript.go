package session

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ListedTranscriptPath preserves the single-instance API. Fleet listings use
// ListedTranscriptPaths to enumerate each Codex home only once.
func ListedTranscriptPath(inst *Instance) string {
	return ListedTranscriptPaths([]*Instance{inst})[inst]
}

// ListedTranscriptPaths resolves stored native identities without pane access
// or transcript body scans. Unknown paths are omitted until Recall opens them.
func ListedTranscriptPaths(instances []*Instance) map[*Instance]string {
	return listedTranscriptPaths(instances, filepath.WalkDir, readCodexRolloutHead)
}

func listedTranscriptPaths(instances []*Instance, walk func(string, fs.WalkDirFunc) error, head func(string) (codexRolloutHead, bool)) map[*Instance]string {
	paths := make(map[*Instance]string)
	homes := make(map[string]map[string][]*Instance)
	for _, inst := range instances {
		if inst == nil || inst.SSHHost != "" {
			continue
		}
		switch {
		case IsClaudeCompatible(inst.Tool) && inst.ClaudeSessionID != "" && inst.TranscriptIsResolvableLocally():
			if path := listedClaudeTranscriptPath(inst); path != "" {
				paths[inst] = path
			}
		case IsCodexCompatible(inst.Tool) && inst.CodexSessionID != "" && inst.CodexRolloutIsResolvableLocally():
			home := CodexHomeDirForInstance(inst)
			if homes[home] == nil {
				homes[home] = make(map[string][]*Instance)
			}
			homes[home][inst.CodexSessionID] = append(homes[home][inst.CodexSessionID], inst)
		}
	}
	for home, requested := range homes {
		root := filepath.Join(home, "sessions")
		_ = walk(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				if entry != nil && entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil || rel == "." {
				return nil
			}
			parts := strings.Split(rel, string(filepath.Separator))
			if entry.IsDir() {
				if len(parts) > 3 || !listedCodexDateDir(parts) {
					return filepath.SkipDir
				}
				return nil
			}
			if len(parts) != 4 || !entry.Type().IsRegular() {
				return nil
			}
			name := entry.Name()
			if !strings.HasPrefix(name, "rollout-") || !strings.HasSuffix(name, ".jsonl") {
				return nil
			}
			stem := strings.TrimSuffix(name, ".jsonl")
			if len(stem) < 37 || stem[len(stem)-37] != '-' {
				return nil
			}
			id := stem[len(stem)-36:]
			matches := requested[id]
			if len(matches) == 0 {
				return nil
			}
			// The first matching path is authoritative, as in the old resolver.
			// Duplicate deck sessions share one bounded header read.
			delete(requested, id)
			if meta, ok := head(path); !ok || !meta.Subagent {
				for _, inst := range matches {
					paths[inst] = path
				}
			}
			if len(requested) == 0 {
				return filepath.SkipAll
			}
			return nil
		})
	}
	return paths
}

func listedCodexDateDir(parts []string) bool {
	for i, part := range parts {
		width := 2
		if i == 0 {
			width = 4
		}
		if len(part) != width {
			return false
		}
		for _, digit := range part {
			if digit < '0' || digit > '9' {
				return false
			}
		}
	}
	return true
}

func listedClaudeTranscriptPath(inst *Instance) string {
	if validateExactSessionID(inst.ClaudeSessionID) != nil {
		return ""
	}
	dir := GetClaudeConfigDirForInstance(inst)
	project := inst.EffectiveWorkingDir()
	projects := []string{project}
	if resolved, err := filepath.EvalSymlinks(project); err == nil && resolved != project {
		projects = append([]string{resolved}, projects...)
	}
	for _, project := range projects {
		encoded := ConvertToClaudeDirName(project)
		if encoded == "" {
			encoded = "-"
		}
		path := filepath.Join(dir, "projects", encoded, inst.ClaudeSessionID+".jsonl")
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			return path
		}
	}
	return ""
}
