package main

import (
	"fmt"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func applyCLIKimiPermissionMode(inst *session.Instance, mode string, explicit bool) error {
	if inst == nil || !explicit {
		return nil
	}
	if inst.Tool != "kimi" {
		return fmt.Errorf("--permission-mode only works with Kimi sessions")
	}
	mode = strings.TrimSpace(mode)
	if err := session.ValidateKimiPermissionMode(mode); err != nil {
		return err
	}
	opts := inst.GetKimiOptions()
	if opts == nil {
		opts = &session.KimiOptions{}
	}
	opts.PermissionMode = mode
	return inst.SetKimiOptions(opts)
}
