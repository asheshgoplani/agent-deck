package session

import (
	"encoding/json"
	"strings"
)

const (
	crossHarnessSupersededByKey = "cross_harness_superseded_by"
	crossHarnessSupersedesKey   = "cross_harness_supersedes"
)

// WriteCrossHarnessLineageToToolData keeps reversible replacement lineage in
// the forward-compatible tool_data extras zone. It deliberately records IDs
// only: no transcript, prompt, account material, or native metadata is copied.
func WriteCrossHarnessLineageToToolData(td json.RawMessage, supersededBy, supersedes string) json.RawMessage {
	fields := map[string]json.RawMessage{}
	_ = json.Unmarshal(td, &fields)
	for key, value := range map[string]string{
		crossHarnessSupersededByKey: strings.TrimSpace(supersededBy),
		crossHarnessSupersedesKey:   strings.TrimSpace(supersedes),
	} {
		encoded, _ := json.Marshal(value)
		fields[key] = encoded
	}
	encoded, err := json.Marshal(fields)
	if err != nil {
		return td
	}
	return encoded
}

func readCrossHarnessLineage(td json.RawMessage, key string) string {
	var fields map[string]json.RawMessage
	if json.Unmarshal(td, &fields) != nil {
		return ""
	}
	var value string
	if json.Unmarshal(fields[key], &value) != nil {
		return ""
	}
	return value
}

func ReadCrossHarnessSupersededByFromToolData(td json.RawMessage) string {
	return readCrossHarnessLineage(td, crossHarnessSupersededByKey)
}

func ReadCrossHarnessSupersedesFromToolData(td json.RawMessage) string {
	return readCrossHarnessLineage(td, crossHarnessSupersedesKey)
}
