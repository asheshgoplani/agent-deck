package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/asheshgoplani/agent-deck/internal/atomicfile"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

type codexHooksInstallPolicy uint8

const (
	codexHooksInstallMissingOnly codexHooksInstallPolicy = iota
	codexHooksInstallOrUpgrade
)

type codexHooksInstallOutcome uint8

const (
	codexHooksOutcomeInstalled codexHooksInstallOutcome = iota
	codexHooksOutcomeUpgraded
	codexHooksOutcomeAlreadyInstalled
	codexHooksOutcomeSkipped
)

type codexHooksInstallResult struct {
	Outcome codexHooksInstallOutcome
	Reason  string
}

// installCodexHooks owns the complete config transaction. Callers own output
// and CLI exits; explicit installation may migrate settings that creation preserves.
func installCodexHooks(configPath string, policy codexHooksInstallPolicy) (codexHooksInstallResult, error) {
	var result codexHooksInstallResult
	lock, err := session.AcquireConfigFileLockTimeout(configPath, time.Second)
	if err != nil {
		return result, fmt.Errorf("locking codex config: %w", err)
	}
	defer lock.Release()
	mode := os.FileMode(0o644)
	if policy == codexHooksInstallMissingOnly {
		mode = 0o600
	}
	if info, err := os.Lstat(configPath); err == nil {
		if policy == codexHooksInstallMissingOnly && !info.Mode().IsRegular() {
			return result, fmt.Errorf("Codex config %s is not a regular file; install hooks manually", configPath)
		}
		target, err := os.Stat(configPath)
		if err != nil {
			// Explicit installation supports a symlink whose target does not
			// exist yet, as the atomic-file writer does. Creation still refuses it.
			if policy != codexHooksInstallOrUpgrade || info.Mode()&os.ModeSymlink == 0 || !os.IsNotExist(err) {
				return result, fmt.Errorf("checking Codex config: %w", err)
			}
		} else {
			if !target.Mode().IsRegular() {
				return result, fmt.Errorf("Codex config %s is not a regular file", configPath)
			}
			mode = target.Mode().Perm()
		}
	} else if !os.IsNotExist(err) {
		return result, fmt.Errorf("checking Codex config: %w", err)
	}
	content, err := readFileOrEmpty(configPath)
	if err != nil {
		return result, fmt.Errorf("reading Codex config: %w", err)
	}
	begin, end := strings.Index(content, codexNotifyMarkerBegin), strings.Index(content, codexNotifyMarkerEnd)
	if (begin >= 0) != (end >= 0) || (begin >= 0 && end < begin) || strings.Count(content, codexNotifyMarkerBegin) > 1 || strings.Count(content, codexNotifyMarkerEnd) > 1 {
		return result, fmt.Errorf("malformed agent-deck Codex hook block in config")
	}
	if begin >= 0 && codexTableHeaderRe.MatchString(content[:begin]) {
		return result, fmt.Errorf("the agent-deck notify block in %s sits below a [table], where notify belongs to that table.\nMove the block above every table, or remove it and rerun 'agent-deck codex-hooks install'", configPath)
	}
	var cfg map[string]any
	if err := toml.Unmarshal([]byte(content), &cfg); err != nil {
		return result, fmt.Errorf("parsing Codex config: %w", err)
	}
	notify, exists := cfg["notify"]
	if policy == codexHooksInstallMissingOnly && exists {
		if isAgentDeckCodexNotify(notify) {
			return codexHooksInstallResult{Outcome: codexHooksOutcomeAlreadyInstalled, Reason: "already installed"}, nil
		}
		return codexHooksInstallResult{Outcome: codexHooksOutcomeSkipped, Reason: "existing notify setting preserved"}, nil
	}
	newline := "\n"
	if strings.Contains(content, "\r\n") {
		newline = "\r\n"
	}
	block := strings.Join([]string{codexNotifyMarkerBegin, codexNotifyLine, codexNotifyMarkerEnd}, newline)
	var updated string
	switch {
	case begin >= 0:
		end += len(codexNotifyMarkerEnd)
		updated = content[:begin] + block + content[end:]
		result.Outcome = codexHooksOutcomeUpgraded
	case policy == codexHooksInstallOrUpgrade && hasLegacyCodexNotifyTable(content):
		remaining, _ := removeLegacyCodexNotifyTable(content)
		updated = prependCodexNotifyBlock(block+newline, remaining)
		result.Outcome = codexHooksOutcomeUpgraded
	case exists && isAgentDeckCodexNotify(notify):
		return codexHooksInstallResult{Outcome: codexHooksOutcomeAlreadyInstalled, Reason: "already installed"}, nil
	case exists:
		return codexHooksInstallResult{Outcome: codexHooksOutcomeSkipped, Reason: "existing notify setting preserved"}, nil
	default:
		updated = block + newline + content
		if policy == codexHooksInstallOrUpgrade && content != "" {
			updated = block + newline + newline + content
		}
	}
	if err := toml.Unmarshal([]byte(updated), &cfg); err != nil {
		return result, fmt.Errorf("validating Codex notify config: %w", err)
	}
	if !isAgentDeckCodexNotify(cfg["notify"]) {
		return result, fmt.Errorf("installed Codex notify setting is not at the config root")
	}
	if _, err := os.Stat(configPath); err == nil {
		f, err := os.OpenFile(configPath, os.O_WRONLY, 0)
		if err != nil {
			return result, fmt.Errorf("opening Codex config for writing: %w", err)
		}
		if err := f.Close(); err != nil {
			return result, fmt.Errorf("closing Codex config: %w", err)
		}
	}
	if err := atomicfile.WriteFileDurable(configPath, []byte(updated), mode); err != nil {
		return result, fmt.Errorf("installing Codex notify hook: %w", err)
	}
	return result, nil
}
func isAgentDeckCodexNotify(value any) bool {
	args, ok := value.([]any)
	return ok && len(args) == 2 && args[0] == "agent-deck" && args[1] == "codex-notify"
}
