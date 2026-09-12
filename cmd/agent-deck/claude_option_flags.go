package main

import (
	"flag"
	"fmt"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// claudeOptionFlags are the CLI twins of the New Session dialog's Claude
// Options rows: Session mode (continue), Skip permissions, Auto mode, Chrome
// mode and Teammate mode. They persist into the session's ClaudeOptions (the
// same store the dialog writes), so a session created from the CLI restarts
// with the same flags, unlike a bare `-c "claude --chrome"`.
type claudeOptionFlags struct {
	continueMode    *bool
	skipPermissions *bool
	autoMode        *bool
	chrome          *bool
	teammateMode    *bool
}

func registerClaudeOptionFlags(fs *flag.FlagSet) *claudeOptionFlags {
	return &claudeOptionFlags{
		continueMode:    fs.Bool("continue", false, "Claude session mode 'continue' (claude -c: resume the most recent conversation in the directory); requires -c claude"),
		skipPermissions: fs.Bool("skip-permissions", false, "Claude: --dangerously-skip-permissions for this session (persisted; requires -c claude)"),
		autoMode:        fs.Bool("auto-mode", false, "Claude: --permission-mode auto for this session (persisted; requires -c claude)"),
		chrome:          fs.Bool("chrome", false, "Claude: --chrome for this session (persisted; requires -c claude)"),
		teammateMode:    fs.Bool("teammate-mode", false, "Claude: --teammate-mode tmux for this session (persisted; requires -c claude)"),
	}
}

func (f *claudeOptionFlags) any() bool {
	return *f.continueMode || *f.skipPermissions || *f.autoMode || *f.chrome || *f.teammateMode
}

// applyCLIClaudeOptionFlags merges the set flags into the session's persisted
// ClaudeOptions. Nothing is written when no flag is set, so tools without
// Claude options are unaffected by the flags' mere existence.
func applyCLIClaudeOptionFlags(inst *session.Instance, f *claudeOptionFlags) error {
	if inst == nil || f == nil || !f.any() {
		return nil
	}
	if !session.IsClaudeCompatible(inst.Tool) {
		return fmt.Errorf("--continue/--skip-permissions/--auto-mode/--chrome/--teammate-mode only apply to claude sessions (tool is %q)", inst.Tool)
	}
	opts := inst.GetClaudeOptions()
	if opts == nil {
		userConfig, _ := session.LoadUserConfig()
		opts = session.NewClaudeOptions(userConfig)
	}
	if *f.continueMode {
		if opts.SessionMode == "resume" {
			return fmt.Errorf("--continue and --resume-session cannot be combined")
		}
		opts.SessionMode = "continue"
	}
	if *f.skipPermissions {
		opts.SkipPermissions = true
	}
	if *f.autoMode {
		opts.AutoMode = true
	}
	if *f.chrome {
		opts.UseChrome = true
	}
	if *f.teammateMode {
		opts.UseTeammateMode = true
	}
	return inst.SetClaudeOptions(opts)
}

// addClaudeOptionsJSON echoes the persisted Claude options next to the model
// and effort keys, so --json consumers see the same values the dialog rows show.
func addClaudeOptionsJSON(target map[string]interface{}, inst *session.Instance) {
	if inst == nil || !session.IsClaudeCompatible(inst.Tool) {
		return
	}
	opts := inst.GetClaudeOptions()
	if opts == nil {
		return
	}
	if opts.SessionMode != "" && opts.SessionMode != "new" {
		target["session_mode"] = opts.SessionMode
	}
	if opts.SkipPermissions {
		target["skip_permissions"] = true
	}
	if opts.AutoMode {
		target["auto_mode"] = true
	}
	if opts.UseChrome {
		target["chrome"] = true
	}
	if opts.UseTeammateMode {
		target["teammate_mode"] = true
	}
}
