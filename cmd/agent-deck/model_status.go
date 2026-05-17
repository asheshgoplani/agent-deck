package main

import (
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func applyCLIModelOverride(inst *session.Instance, modelID string) error {
	modelID = strings.TrimSpace(modelID)
	if inst == nil || modelID == "" {
		return nil
	}
	return inst.ApplyLaunchModel(modelID)
}

func addModelInfoJSON(target map[string]interface{}, info session.ModelInfo) {
	if info.ModelID == "" {
		return
	}
	target["model_id"] = info.ModelID
	if info.Model != "" {
		target["model"] = info.Model
	}
	if info.Version != "" {
		target["model_version"] = info.Version
	}
}

func modelStatusDisplay(inst *session.Instance) string {
	if inst == nil {
		return "-"
	}
	info := inst.LaunchModelInfo()
	if info.ModelID != "" {
		return info.Display()
	}
	if session.SupportsLaunchModel(inst.Tool) {
		return "tool default"
	}
	return "-"
}
