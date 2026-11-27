package wire

import (
	"encoding/json"
	"sort"
	"strings"
	"unicode/utf8"
)

// MaxBatchOperations caps one batch: its schema, its validation and its
// resume index all use this value.
const MaxBatchOperations = 24

// MaxParallelBatchChildren bounds how many read-only children a parallel
// batch runs at once, independent of the operation cap.
const MaxParallelBatchChildren = 12

func TruncateUTF8(value string, maxBytes int, marker string) (string, bool) {
	value = strings.ToValidUTF8(value, "\uFFFD")
	if len(value) <= maxBytes {
		return value, false
	}
	if maxBytes <= 0 {
		return "", true
	}
	suffix := ""
	budget := maxBytes
	if len(marker) <= maxBytes {
		suffix = marker
		budget -= len(marker)
	}
	end := min(len(value), budget)
	for end > 0 && end < len(value) && !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end] + suffix, true
}
func utf16Prefix(value string, units int) string {
	used, end := 0, 0
	for end < len(value) {
		r, size := utf8.DecodeRuneInString(value[end:])
		count := 1
		if r > 0xffff {
			count = 2
		}
		if used+count > units {
			break
		}
		used += count
		end += size
	}
	return value[:end]
}
func CompactStructured(value any) any { return compactAt(value, 0) }
func compactAt(value any, depth int) any {
	if depth > 8 || value == nil {
		return value
	}
	switch v := value.(type) {
	case string:
		prefix := utf16Prefix(v, 30000)
		if len(prefix) < len(v) {
			return prefix + "\n...[structured field truncated to 30000 chars]"
		}
		return v
	case []any:
		out := make([]any, len(v))
		for i, x := range v {
			out[i] = compactAt(x, depth+1)
		}
		return out
	case []map[string]any:
		out := make([]any, len(v))
		for i, x := range v {
			out[i] = compactAt(x, depth+1)
		}
		return out
	case []string:
		out := make([]any, len(v))
		for i, x := range v {
			out[i] = compactAt(x, depth+1)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, x := range v {
			out[k] = compactAt(x, depth+1)
		}
		return out
	default:
		return value
	}
}

var BatchPriority = strings.Split("workspace_id project_id root path paths error error_code retry_unchanged recovery provider provider_version mode language edit_tag base_edit_tag sha256 start_line end_line next_start_line total_lines bytes returned_bytes changed created existed additions deletions replacements edits_applied exit_code signal duration_ms timed_out truncated has_more next_cursor query_fingerprint count entries matches_count matches contexts warnings changed_paths operation_count succeeded_count failed_count skipped_count succeeded stdout stderr text diff", " ")

func JSONValue(value any) any {
	b, e := json.Marshal(value)
	if e != nil {
		return nil
	}
	var out any
	if json.Unmarshal(b, &out) != nil {
		return nil
	}
	return out
}
func BoundedStructured(value any, maxBytes int) (any, bool) {
	compact := CompactStructured(JSONValue(value))
	size := func(v any) int {
		b, e := json.Marshal(v)
		if e != nil {
			return int(^uint(0) >> 1)
		}
		return len(b)
	}
	if size(compact) <= maxBytes {
		return compact, false
	}
	stringBytes := max(64, min(2000, maxBytes/3))
	arrayItems, objectKeys := 20, 40
	ranks := map[string]int{}
	for i, k := range BatchPriority {
		ranks[k] = i
	}
	var trim func(any, int) any
	trim = func(v any, depth int) any {
		switch x := v.(type) {
		case nil, bool, float64, json.Number:
			return x
		case string:
			t, _ := TruncateUTF8(x, stringBytes, "…")
			return t
		}
		if depth >= 5 {
			return "[batch depth limit]"
		}
		switch x := v.(type) {
		case []any:
			out := make([]any, min(len(x), arrayItems))
			for i := range out {
				out[i] = trim(x[i], depth+1)
			}
			return out
		case map[string]any:
			keys := make([]string, 0, len(x))
			for k := range x {
				keys = append(keys, k)
			}
			sort.Slice(keys, func(i, j int) bool {
				a, ok := ranks[keys[i]]
				if !ok {
					a = 9999
				}
				b, ok := ranks[keys[j]]
				if !ok {
					b = 9999
				}
				if a != b {
					return a < b
				}
				return keys[i] < keys[j]
			})
			out := map[string]any{}
			for _, k := range keys[:min(len(keys), objectKeys)] {
				out[k] = trim(x[k], depth+1)
			}
			return out
		default:
			return v
		}
	}
	for attempt := 0; attempt < 6; attempt++ {
		candidate := trim(compact, 0)
		if size(candidate) <= maxBytes {
			return candidate, true
		}
		stringBytes = max(32, stringBytes/2)
		arrayItems = max(1, arrayItems/2)
		objectKeys = max(4, objectKeys/2)
	}
	summary := map[string]any{"_batch_truncated": true}
	if object, ok := compact.(map[string]any); ok {
		for _, key := range BatchPriority {
			item, ok := object[key]
			if !ok {
				continue
			}
			switch v := item.(type) {
			case []any, map[string]any:
				continue
			case string:
				item, _ = TruncateUTF8(v, 128, "…")
			}
			summary[key] = item
			if size(summary) > maxBytes {
				delete(summary, key)
				break
			}
		}
	}
	return summary, true
}
