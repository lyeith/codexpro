package contracts

import (
	"encoding/json"
	"fmt"
	"strings"
)

const capacityQueueDescription = " Optional admission=queue durably accepts bounded FIFO waiting jobs. It requires a stable queue_key: retry that same key/payload to retrieve the same IDs, including finished jobs, until queue_key_expires_at. A different payload with the key is refused. Queued jobs never consume execution slots or run timeouts; jobs and stop_jobs collect/cancel the same IDs. Default admission=reject keeps the existing full-capacity refusal."

// Apply once at the common registry boundary, including managed-workspace
// routing overrides and the second configured-registry pass. Frozen captures
// remain untouched; this is one explicitly maintained contract extension.
func withCapacityQueue(data json.RawMessage) (json.RawMessage, error) {
	var tool map[string]any
	if err := json.Unmarshal(data, &tool); err != nil {
		return nil, err
	}
	schema, ok := tool["inputSchema"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("job schema missing")
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("job properties missing")
	}
	description, ok := tool["description"].(string)
	if !ok {
		return nil, fmt.Errorf("job description missing")
	}
	tool["description"] = strings.TrimSuffix(description, capacityQueueDescription) + capacityQueueDescription
	props["admission"] = map[string]any{"type": "string", "enum": []string{"reject", "queue"}, "description": "Default reject. queue accepts up to four times the configured global/per-workspace execution limits as pending jobs; oldest eligible queued work starts first."}
	props["queue_key"] = map[string]any{"type": "string", "minLength": 1, "maxLength": 160, "description": "Required for admission=queue. Stable key scoped to this named caller and workspace; never change it to retry an uncertain submission. Distinct keys authorize distinct submissions in queued mode."}
	props["queue_timeout_ms"] = map[string]any{"type": "integer", "minimum": 1000, "maximum": 86400000, "description": "Maximum capacity wait, default one hour; independent of the existing execution timeout. Requires admission=queue."}
	return json.Marshal(tool)
}
