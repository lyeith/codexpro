package batch

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

var structuredPriority = []string{
	"workspace_id", "project_id", "root", "path", "paths", "error", "error_code", "retry_unchanged", "recovery",
	"provider", "provider_version", "mode", "language", "edit_tag", "base_edit_tag", "sha256",
	"start_line", "end_line", "next_start_line", "total_lines", "bytes", "returned_bytes", "changed", "created", "existed",
	"additions", "deletions", "replacements", "edits_applied", "exit_code", "signal", "duration_ms",
	"timed_out", "truncated", "has_more", "next_cursor", "query_fingerprint", "count", "entries", "matches_count",
	"matches", "contexts", "warnings", "changed_paths", "operation_count", "succeeded_count", "failed_count",
	"skipped_count", "succeeded", "stdout", "stderr", "text", "diff",
}

func jsonBytes(value any) ([]byte, error) {
	var b bytes.Buffer
	encoder := json.NewEncoder(&b)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte{'\n'}), nil
}
func fits(value any, budget int) bool {
	b, err := jsonBytes(value)
	return err == nil && len(b) <= budget
}

func truncate(value string, maxBytes int, marker string) (string, bool) {
	value = strings.ToValidUTF8(value, "�")
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

func prefixCharacters(s string, units int) string {
	used, end := 0, 0
	for end < len(s) {
		r, size := utf8.DecodeRuneInString(s[end:])
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
	return s[:end]
}

func compact(value any, depth int) any {
	if depth > 8 || value == nil {
		return value
	}
	switch v := value.(type) {
	case string:
		prefix := prefixCharacters(v, 30000)
		if len(prefix) < len(v) {
			return prefix + "\n...[structured field truncated to 30000 chars]"
		}
		return v
	case []any:
		out := make([]any, len(v))
		for i, x := range v {
			out[i] = compact(x, depth+1)
		}
		return out
	case []map[string]any:
		out := make([]any, len(v))
		for i, x := range v {
			out[i] = compact(x, depth+1)
		}
		return out
	case []string:
		out := make([]any, len(v))
		for i, x := range v {
			out[i] = compact(x, depth+1)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, x := range v {
			out[k] = compact(x, depth+1)
		}
		return out
	default:
		return value
	}
}

func boundedStructured(value any, budget int) (any, bool) {
	value = compact(value, 0)
	if fits(value, budget) {
		return value, false
	}
	ranks := map[string]int{}
	for i, key := range structuredPriority {
		ranks[key] = i
	}
	stringBytes := max(64, min(2000, budget/3))
	arrayItems, objectKeys := 20, 40
	var trim func(any, int) any
	trim = func(candidate any, depth int) any {
		switch x := candidate.(type) {
		case nil, bool, int, int64, float64, json.Number:
			return x
		case string:
			s, _ := truncate(x, stringBytes, "…")
			return s
		}
		if depth >= 5 {
			return "[batch depth limit]"
		}
		switch x := candidate.(type) {
		case []any:
			n := min(len(x), arrayItems)
			out := make([]any, n)
			for i := range out {
				out[i] = trim(x[i], depth+1)
			}
			return out
		case map[string]any:
			keys := make([]string, 0, len(x))
			for key := range x {
				keys = append(keys, key)
			}
			sort.Slice(keys, func(i, j int) bool {
				a, aok := ranks[keys[i]]
				b, bok := ranks[keys[j]]
				if !aok {
					a = 1 << 30
				}
				if !bok {
					b = 1 << 30
				}
				if a != b {
					return a < b
				}
				return keys[i] < keys[j]
			})
			if len(keys) > objectKeys {
				keys = keys[:objectKeys]
			}
			out := map[string]any{}
			for _, key := range keys {
				out[key] = trim(x[key], depth+1)
			}
			return out
		default:
			return fmt.Sprint(candidate)
		}
	}
	for i := 0; i < 6; i++ {
		candidate := trim(value, 0)
		if fits(candidate, budget) {
			return candidate, true
		}
		stringBytes = max(32, stringBytes/2)
		arrayItems = max(1, arrayItems/2)
		objectKeys = max(4, objectKeys/2)
	}
	summary := map[string]any{"_batch_truncated": true}
	if source, ok := value.(map[string]any); ok {
		for _, key := range structuredPriority {
			item, exists := source[key]
			if !exists {
				continue
			}
			switch item.(type) {
			case map[string]any, []any, []string, []map[string]any:
				continue
			}
			if text, ok := item.(string); ok {
				item, _ = truncate(text, 128, "…")
			}
			summary[key] = item
			if !fits(summary, budget) {
				delete(summary, key)
				break
			}
		}
	}
	return summary, true
}
