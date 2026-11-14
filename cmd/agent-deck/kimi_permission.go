package main

import (
	"fmt"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// applyCLIKimiPermissionFlags resolves Kimi's --yolo/--auto flags into a
// per-session permission mode. Non-Kimi instances are a no-op. Both flags set
// is an error. Neither set leaves the mode empty so the session inherits the
// global [kimi].permission_mode.
func applyCLIKimiPermissionFlags(inst *session.Instance, yolo, auto bool, yoloSet, autoSet bool) error {
	if inst == nil || inst.Tool != "kimi" {
		return nil
	}
	if yoloSet && autoSet {
		return fmt.Errorf("--auto (Never Ask) and --yolo (Ask When Needed) are mutually exclusive; pick one")
	}
	if !yoloSet && !autoSet {
		return nil
	}
	mode := session.KimiPermissionAskWhenNeeded
	if autoSet {
		mode = session.KimiPermissionNeverAsk
	}
	opts := inst.GetKimiOptions()
	if opts == nil {
		opts = &session.KimiOptions{}
	}
	opts.PermissionMode = mode
	return inst.SetKimiOptions(opts)
}
