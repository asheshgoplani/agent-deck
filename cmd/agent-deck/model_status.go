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

// applyCLIEffortOverride mirrors the new-session dialog's "Reasoning effort"
// row: a per-session native effort level for claude (--effort) and codex
// (model_reasoning_effort). Empty leaves the tool default.
func applyCLIEffortOverride(inst *session.Instance, effort string) error {
	effort = strings.TrimSpace(effort)
	if inst == nil || effort == "" {
		return nil
	}
	return inst.ApplyLaunchReasoningEffort(effort)
}

// addEffortJSON surfaces the persisted per-session reasoning effort next to
// the model fields, so --json consumers see the same value the TUI row shows.
func addEffortJSON(target map[string]interface{}, inst *session.Instance) {
	if effort := inst.LaunchReasoningEffort(); effort != "" {
		target["effort"] = effort
	}
}

func addModelInfoJSON(target map[string]interface{}, info session.ModelInfo) {
	if info.ModelID == "" {
		return
	}
	target["model_id"] = info.ModelID
	// JSON is a data surface: return the exact persisted override. Family names
	// such as "GPT" are presentation labels and belong in human output only.
	target["model"] = info.ModelID
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
