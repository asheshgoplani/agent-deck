package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/asheshgoplani/agent-deck/internal/atomicfile"
)

type usageFeedBackup struct {
	Command         string          `json:"installed_command"`
	PreviousCommand string          `json:"previous_installed_command,omitempty"`
	Original        json.RawMessage `json:"original_statusline"`
}

func usageFeedBackupPath(configDir string) string {
	return filepath.Join(configDir, "agent-deck-statusline-backup.json")
}

func readUsageFeedBackup(configDir string) (usageFeedBackup, error) {
	var backup usageFeedBackup
	data, err := os.ReadFile(usageFeedBackupPath(configDir))
	if err != nil {
		return backup, err
	}
	err = json.Unmarshal(data, &backup)
	return backup, err
}

// Save before modifying settings. Repinning/upgrading our own wrapper keeps
// the original, while a user-replaced command becomes the new original.
func saveUsageFeedBackup(configDir string, root jsonObject, current, next string) error {
	backup, err := readUsageFeedBackup(configDir)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read statusline backup: %w", err)
	}
	if (backup.Command != current && backup.PreviousCommand != current) || backup.Command == "" {
		backup.Original, _ = root.get("statusLine")
		if parsed, ok := parseUsageFeedCommand(current); ok {
			var original jsonObject
			if err := json.Unmarshal(backup.Original, &original); err != nil {
				return err
			}
			if parsed.Inner != "" {
				original.set("command", mustMarshal(parsed.Inner))
			} else {
				original.del("command")
				if original.getString("type") == "command" {
					original.del("type")
				}
			}
			backup.Original = nil
			if len(original) > 0 {
				backup.Original = mustMarshal(original)
			}
		}
	}
	backup.PreviousCommand = current
	backup.Command = next
	data, err := json.Marshal(backup)
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(usageFeedBackupPath(configDir), data, 0600)
}

// Restore only the keys the wrapper changes, retaining later padding or other
// statusline edits. A user-replaced command is never overwritten.
func restoreUsageFeedBackup(configDir string, root *jsonObject, obj jsonObject) (bool, error) {
	backup, err := readUsageFeedBackup(configDir)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if backup.Command != obj.getString("command") && backup.PreviousCommand != obj.getString("command") {
		return false, nil
	}
	var original jsonObject
	if len(backup.Original) > 0 && string(backup.Original) != "null" {
		if err := json.Unmarshal(backup.Original, &original); err != nil {
			return false, err
		}
	}
	for _, key := range []string{"command", "type"} {
		if value, ok := original.get(key); ok {
			obj.set(key, value)
		} else {
			obj.del(key)
		}
	}
	if len(obj) == 0 && (len(backup.Original) == 0 || string(backup.Original) == "null") {
		root.del("statusLine")
	} else {
		root.set("statusLine", mustMarshal(obj))
	}
	return true, nil
}
