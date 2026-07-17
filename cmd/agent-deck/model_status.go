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

// addAutoNameJSON surfaces a session's auto-name state on `session show --json`
// so consumers (notably the notification hook) can show the meaningful task
// description instead of the machine-generated handle in Title. auto_name is
// always present; auto_name_description only when a description was captured.
func addAutoNameJSON(target map[string]interface{}, inst *session.Instance) {
	if inst == nil {
		return
	}
	target["auto_name"] = inst.GetAutoName()
	if desc := inst.GetAutoNameDescription(); desc != "" {
		target["auto_name_description"] = desc
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
