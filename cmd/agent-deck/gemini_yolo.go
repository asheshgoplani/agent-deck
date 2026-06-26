package main

import (
	"fmt"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func applyCLIYoloOverride(inst *session.Instance, enabled bool) error {
	if !enabled || inst == nil {
		return nil
	}
	switch inst.Tool {
	case "gemini":
		inst.SetGeminiYoloMode(true)
	case "antigravity":
		inst.SetAntigravityYoloMode(true)
	case "codex":
		yolo := true
		opts := inst.GetCodexOptions()
		if opts == nil {
			opts = &session.CodexOptions{}
		}
		opts.YoloMode = &yolo
		if err := inst.SetCodexOptions(opts); err != nil {
			return err
		}
	default:
		return fmt.Errorf("--yolo only works with Gemini, Antigravity, or Codex sessions")
	}
	return nil
}
