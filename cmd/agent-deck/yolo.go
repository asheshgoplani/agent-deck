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
	case "copilot":
		yolo := true
		opts := inst.GetCopilotOptions()
		if opts == nil {
			opts = &session.CopilotOptions{}
		}
		opts.YoloMode = &yolo
		if err := inst.SetCopilotOptions(opts); err != nil {
			return err
		}
	default:
		return fmt.Errorf("--yolo only works with Gemini, Codex, or Copilot sessions")
	}
	return nil
}
