package session

import (
	"errors"
	"fmt"
	"sort"
)

// Sentinel errors retained for compatibility with the account-only CLI API.
var (
	ErrUnknownAccount             = errors.New("unknown account slot")
	ErrAccountSwitchUnsupported   = errors.New("account switch unsupported for this tool")
	ErrAccountSwitchRestartFailed = errors.New("restart after account switch failed")
)

type AccountSwitchOptions struct {
	NoRestart bool
}

type AccountSwitchResult struct {
	OldAccount   string
	NewAccount   string
	MigratedPath string
	Conversation string
	Restarted    bool
	Warnings     []string

	// nativeResult is the executor-issued storage-CAS capability. It is kept
	// private so compatibility callers cannot construct a result that commits
	// an unbound account mutation.
	nativeResult *HarnessSwitchResult
}

// ConfiguredAccountNames lists profile names with a Claude config_dir binding.
// Presence is configuration evidence only; it does not verify OAuth state.
func ConfiguredAccountNames(cfg *UserConfig) []string {
	if cfg == nil {
		return nil
	}
	var names []string
	for name := range cfg.Profiles {
		if cfg.GetProfileClaudeConfigDir(name) != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// SwitchAccount is the compatibility adapter used by older callers. All
// mutations now go through ExecuteHarnessSwitch, which performs immutable
// exact-ID preflight, source-preserving staging, journaling, locking, and
// destination verification before committing the account field.
func SwitchAccount(cfg *UserConfig, inst *Instance, account string, opts AccountSwitchOptions) (*AccountSwitchResult, error) {
	if inst == nil {
		return nil, errors.New("no session")
	}
	// Preserve the account-only API's sentinel errors while the shared
	// executor remains the single mutation path.
	preview := PreviewSwitch(cfg, inst, SwitchPreviewTarget{Harness: inst.Tool, Account: account})
	if preview.Refusal != nil {
		sentinel := ErrAccountSwitchUnsupported
		if preview.Refusal.Code == "unknown-account" {
			sentinel = ErrUnknownAccount
		}
		return nil, fmt.Errorf("%w: %s", sentinel, preview.Refusal.Message)
	}
	result, err := ExecuteHarnessSwitch(cfg, inst, HarnessSwitchOptions{
		Target:  SwitchPreviewTarget{Harness: inst.Tool, Account: account},
		NoStart: opts.NoRestart,
	})
	if result == nil {
		return nil, err
	}
	if !result.Committed {
		if err == nil {
			err = errors.New("switch did not commit")
		}
		return nil, fmt.Errorf("%w: %v", ErrAccountSwitchRestartFailed, err)
	}
	conversation := result.Conversation
	if conversation == "" {
		conversation = "no conversation to migrate (fresh session)"
	}
	return &AccountSwitchResult{
		OldAccount:   result.OldAccount,
		NewAccount:   result.NewAccount,
		MigratedPath: result.DestinationPath,
		Conversation: conversation,
		Restarted:    result.Restarted,
		Warnings:     result.Warnings,
		nativeResult: result,
	}, err
}

// CommitAccountSwitch persists a successful compatibility account switch with
// the same scoped native-result CAS used by the unified CLI and TUI. It must
// replace any legacy full-registry SaveWithGroups call: lifecycle monitoring
// may have changed status or unrelated rows after SwitchAccount loaded them.
func CommitAccountSwitch(storage *Storage, inst *Instance, result *AccountSwitchResult) error {
	if result == nil || result.nativeResult == nil {
		return errors.New("account switch storage commit is incomplete")
	}
	return storage.CommitNativeHarnessSwitch(inst, result.nativeResult)
}

// AccountSwitchRestartUnsafe remains a public diagnostic helper for callers
// that want to reject manual restarts without an exact conversation binding.
func AccountSwitchRestartUnsafe(inst *Instance, locatedDir string, wasRunning, noRestart bool) (bool, string) {
	if inst == nil || !wasRunning || noRestart {
		return false, ""
	}
	if locatedDir != "" || inst.ClaudeSessionID != "" || inst.ClaudeDetectedAt.IsZero() {
		return false, ""
	}
	return true, "this session previously held a Claude conversation, but none was found in any " +
		"configured config dir and it has no recorded conversation id; account not switched and " +
		"session NOT restarted (restarting in this state can adopt a transcript belonging to another " +
		"session in the same directory). Re-run with --no-restart to switch the account only, then " +
		"start it manually once the conversation is located"
}
