package main

import (
	"fmt"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// applyCLIKimiPermissionFlags resolves Kimi's --yolo/--auto flags into a
// per-session permission mode. Non-Kimi instances reject --auto and otherwise
// no-op. Enabling both flags is an error. Neither set leaves the mode empty so
// the session inherits the global [kimi].permission_mode; an explicit false
// selects Always Ask.
func applyCLIKimiPermissionFlags(inst *session.Instance, yolo, auto bool, yoloSet, autoSet bool) error {
	if inst == nil {
		return nil
	}
	if autoSet && inst.Tool != "kimi" {
		return fmt.Errorf("--auto only works with Kimi sessions")
	}
	if inst.Tool != "kimi" {
		return nil
	}
	if yolo && auto {
		return fmt.Errorf("--auto (Never Ask) and --yolo (Ask When Needed) are mutually exclusive; pick one")
	}
	if !yoloSet && !autoSet {
		return nil
	}
	mode := session.KimiPermissionAlwaysAsk
	if yolo {
		mode = session.KimiPermissionAskWhenNeeded
	} else if auto {
		mode = session.KimiPermissionNeverAsk
	}
	opts := inst.GetKimiOptions()
	if opts == nil {
		opts = &session.KimiOptions{}
	}
	opts.PermissionMode = mode
	return inst.SetKimiOptions(opts)
}
