package main

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// `session show --json` must surface the auto-name fields so the notification
// hook can show the meaningful task description (auto_name_description) instead
// of the machine-generated handle (title). The description key is present only
// when non-empty; auto_name is always present.
func TestAddAutoNameJSON_PresentWhenSet(t *testing.T) {
	inst := session.NewInstanceWithTool("azure-spruce", "/tmp/x", "claude")
	inst.SetAutoName(true)
	inst.SetAutoNameDescription("Add activity logging for projects")

	jsonData := map[string]interface{}{}
	addAutoNameJSON(jsonData, inst)

	if got, ok := jsonData["auto_name"].(bool); !ok || !got {
		t.Fatalf("auto_name = %v (ok=%v), want true", jsonData["auto_name"], ok)
	}
	if got := jsonData["auto_name_description"]; got != "Add activity logging for projects" {
		t.Fatalf("auto_name_description = %v, want the task description", got)
	}
}

// When there is no captured description, the description key is omitted (absence
// is unambiguous) but auto_name still reports the flag.
func TestAddAutoNameJSON_OmitsEmptyDescription(t *testing.T) {
	inst := session.NewInstanceWithTool("plain", "/tmp/y", "claude")

	jsonData := map[string]interface{}{}
	addAutoNameJSON(jsonData, inst)

	if got, ok := jsonData["auto_name"].(bool); !ok || got {
		t.Fatalf("auto_name = %v (ok=%v), want false", jsonData["auto_name"], ok)
	}
	if _, present := jsonData["auto_name_description"]; present {
		t.Fatalf("auto_name_description must be omitted when empty, got %v", jsonData["auto_name_description"])
	}
}
