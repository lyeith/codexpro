package audit

// cloneMetadata detaches the bounded metadata tree before it enters the writer
// queue. Large raw request contents and full tool output are never cloned.
// A caller whose durable wait times out can safely finish/redact its own DTOs
// while the accepted journal append continues in the background.
func cloneMetadata(value any) any {
	switch value := value.(type) {
	case Action:
		if value == nil {
			return Action(nil)
		}
		out := make(Action, len(value))
		for key, item := range value {
			out[key] = cloneMetadata(item)
		}
		return out
	case map[string]any:
		if value == nil {
			return map[string]any(nil)
		}
		out := make(map[string]any, len(value))
		for key, item := range value {
			out[key] = cloneMetadata(item)
		}
		return out
	case []any:
		if value == nil {
			return []any(nil)
		}
		out := make([]any, len(value))
		for i, item := range value {
			out[i] = cloneMetadata(item)
		}
		return out
	case []map[string]any:
		if value == nil {
			return []map[string]any(nil)
		}
		out := make([]map[string]any, len(value))
		for i, item := range value {
			out[i] = cloneMetadata(item).(map[string]any)
		}
		return out
	case []Action:
		if value == nil {
			return []Action(nil)
		}
		out := make([]Action, len(value))
		for i, item := range value {
			out[i] = cloneMetadata(item).(Action)
		}
		return out
	case []string:
		if value == nil {
			return []string(nil)
		}
		out := make([]string, len(value))
		copy(out, value)
		return out
	default:
		return value
	}
}
