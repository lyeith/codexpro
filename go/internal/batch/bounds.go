package batch

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/lyeith/codexpro/go/internal/wire"
)

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
	return wire.TruncateUTF8(value, maxBytes, marker)
}

func boundedStructured(value any, budget int) (any, bool) {
	value = wire.CompactStructured(value)
	if fits(value, budget) {
		return value, false
	}
	ranks := map[string]int{}
	for i, key := range wire.BatchPriority {
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
		for _, key := range wire.BatchPriority {
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
