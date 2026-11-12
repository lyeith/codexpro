package contracts

import (
	"encoding/json"
	"fmt"
)

const capacityQueueDescription = "Durably queue background commands with bounded FIFO capacity waiting. Requires a stable queue_key scoped to this named caller and workspace: retry the same key and payload to retrieve the same IDs, including finished jobs, until queue_key_expires_at. A different payload is refused. Queued jobs consume neither execution slots nor execution timeout; jobs and stop_jobs collect/cancel these IDs. A distinct key authorizes a distinct submission. Older servers refuse this tool; never fall back to start_jobs after an uncertain response."

func withCapacityQueue(raw []json.RawMessage) ([]json.RawMessage, error) {
	out := make([]json.RawMessage, 0, len(raw)+1)
	for _, data := range raw {
		out = append(out, data)
		var tool map[string]any
		if err := json.Unmarshal(data, &tool); err != nil {
			return nil, err
		}
		if tool["name"] != "start_jobs" {
			continue
		}
		schema, ok := tool["inputSchema"].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("job schema missing")
		}
		props, ok := schema["properties"].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("job properties missing")
		}
		tool["name"] = "queue_jobs"
		tool["title"] = "Queue Background Jobs"
		tool["description"] = capacityQueueDescription
		props["queue_key"] = map[string]any{"type": "string", "minLength": 1, "maxLength": 160, "description": "Stable caller/workspace submission key. Never change this key to retry an uncertain submission."}
		props["queue_timeout_ms"] = map[string]any{"type": "integer", "minimum": 1000, "maximum": 86400000, "description": "Maximum capacity wait, default one hour, independent of execution timeout."}
		required, _ := schema["required"].([]any)
		schema["required"] = append(required, "queue_key")
		queued, err := json.Marshal(tool)
		if err != nil {
			return nil, err
		}
		out = append(out, queued)
	}
	return out, nil
}
