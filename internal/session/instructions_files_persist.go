// Instruction-file list (issue #3206) persistence.
//
// Follows context_level_persist.go: the list lives in the tool_data extras
// zone, outside the positional MarshalToolData signature and without a SQL
// schema migration. The key is deleted when the list is empty, so a cleared
// list and a legacy row read the same.
package session

import "encoding/json"

// WriteInstructionsFilesToToolData merges the instruction-file list into the
// given tool_data blob, removing the key when the list is empty. Sibling keys
// are preserved; a blob that cannot be re-encoded is returned unchanged.
func WriteInstructionsFilesToToolData(td json.RawMessage, files []string) json.RawMessage {
	m := map[string]json.RawMessage{}
	if len(td) > 0 {
		_ = json.Unmarshal(td, &m)
	}
	if len(files) == 0 {
		delete(m, toolDataInstructionsFilesKey)
	} else {
		raw, err := json.Marshal(files)
		if err != nil {
			return td
		}
		m[toolDataInstructionsFilesKey] = raw
	}
	out, err := json.Marshal(m)
	if err != nil {
		return td
	}
	return out
}

// ReadInstructionsFilesFromToolData extracts the instruction-file list.
// Returns nil for missing, malformed or legacy rows.
func ReadInstructionsFilesFromToolData(td json.RawMessage) []string {
	if len(td) == 0 {
		return nil
	}
	var blob struct {
		InstructionsFiles []string `json:"instructions_files"`
	}
	if err := json.Unmarshal(td, &blob); err != nil || len(blob.InstructionsFiles) == 0 {
		return nil
	}
	return blob.InstructionsFiles
}
