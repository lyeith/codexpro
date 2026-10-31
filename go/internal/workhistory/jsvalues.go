package workhistory

import (
	"encoding/json"
	"reflect"
	"sort"

	"github.com/lyeith/codexpro/go/internal/wire"
)

// JavaScript keeps undefined-valued object properties in Object.keys while
// JSON.stringify omits them. The release's bounded formatter sorts and slices
// those keys before serialization, so replacing undefined with absence or null
// changes which real fields survive. This marker exists only inside formatting.
type workUndefinedValue struct{}

var workUndefined = workUndefinedValue{}

func isWorkUndefined(value any) bool {
	_, ok := value.(workUndefinedValue)
	return ok
}

func workJSONValue(value any, keepUndefined bool) any {
	if isWorkUndefined(value) {
		if keepUndefined {
			return value
		}
		return nil
	}
	if result, ok := value.(wire.Result); ok {
		// Avoid wire.Result.MarshalJSON's HTML-escaping encoder when measuring
		// the release's JSON.stringify(textResult(...)) byte budget.
		body := map[string]any{"content": result.Content}
		if result.Meta != nil {
			body["_meta"] = result.Meta
		}
		if result.StructuredContent != nil {
			body["structuredContent"] = result.StructuredContent
		}
		if result.IsError {
			body["isError"] = true
		}
		return workJSONValue(body, keepUndefined)
	}
	if value == nil {
		return nil
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Map:
		if v.IsNil() {
			return nil
		}
		if v.Type().Key().Kind() != reflect.String {
			return value
		}
		out := make(map[string]any, v.Len())
		iterator := v.MapRange()
		for iterator.Next() {
			item := iterator.Value().Interface()
			if !keepUndefined && isWorkUndefined(item) {
				continue
			}
			out[iterator.Key().String()] = workJSONValue(item, keepUndefined)
		}
		return out
	case reflect.Array, reflect.Slice:
		if v.Kind() == reflect.Slice && v.IsNil() {
			return nil
		}
		out := make([]any, v.Len())
		for index := range out {
			out[index] = workJSONValue(v.Index(index).Interface(), keepUndefined)
		}
		return out
	default:
		return value
	}
}

func workCopy(value map[string]any) map[string]any {
	if value == nil {
		return nil
	}
	return workJSONValue(value, true).(map[string]any)
}

func workTruthy(value any) bool {
	if value == nil || isWorkUndefined(value) {
		return false
	}
	switch v := value.(type) {
	case bool:
		return v
	case string:
		return v != ""
	}
	return true
}

func workMissingFields(value map[string]any, keys ...string) {
	for _, key := range keys {
		if _, present := value[key]; !present {
			value[key] = workUndefined
		}
	}
}

// Restore only object properties explicitly created by the pinned coordinator
// summary/status methods. The Go store retains normal JSON and stays untouched.
// These properties are omitted again at the public serialization boundary.
func workSummaryShape(summary map[string]any) {
	workMissingFields(summary, "run_id", "project_id", "context_dir", "title", "mode", "state", "health", "revision", "plan_revision",
		"spec_revision", "workspace_id", "branch", "iteration_id", "generation", "limits", "attempt_count", "updated_at", "recovery_reason",
		"todos", "claimed", "inflight", "unresolved_operations")
	if health, ok := summary["health"].(map[string]any); ok {
		workMissingFields(health, "state", "needs_attention", "observed_at", "last_contact_at", "last_progress_at",
			"idle_remaining_ms", "attempt_remaining_ms", "no_progress_count", "note")
	}
}

func workResultShape(value map[string]any) map[string]any {
	value = workCopy(value)
	if _, isSummary := value["health"].(map[string]any); isSummary {
		if _, hasRun := value["run_id"]; hasRun {
			workSummaryShape(value)
		}
	}
	if runs, ok := value["runs"].([]any); ok {
		for _, raw := range runs {
			if summary, ok := raw.(map[string]any); ok {
				workSummaryShape(summary)
			}
		}
	}
	if packet, ok := value["packet"].(map[string]any); ok {
		workMissingFields(value, "mode", "limits", "server_time", "current_iteration", "recent_iterations", "operations", "jobs",
			"completion", "completion_matches_current_source")
		workMissingFields(packet, "objective", "scope", "acceptance", "todos", "checkpoint", "documents", "source", "activity", "guidance")
		if jobs, ok := value["jobs"].([]any); ok {
			for _, raw := range jobs {
				if job, ok := raw.(map[string]any); ok {
					workMissingFields(job, "job_id", "status", "quiescent", "operation_id", "started_at", "finished_at", "deadline_ms")
				}
			}
		}
	}
	return value
}

// This is the release boundedBatchStructuredContent algorithm over work's
// intermediate JS object shape. The shared wire helper intentionally accepts
// ordinary JSON; sending this shape through JSONValue first would erase keys.
func boundedWorkStructured(value any, maxBytes int) (any, bool) {
	compact := wire.CompactStructured(workJSONValue(value, true))
	if jsonBytes(compact) <= maxBytes {
		return compact, false
	}
	stringBytes := max(64, min(2000, maxBytes/3))
	arrayItems, objectKeys := 20, 40
	ranks := map[string]int{}
	for index, key := range wire.BatchPriority {
		ranks[key] = index
	}
	var trim func(any, int) any
	trim = func(candidate any, depth int) any {
		if isWorkUndefined(candidate) {
			return candidate
		}
		switch value := candidate.(type) {
		case nil, bool, float64, float32, json.Number, int, int64, int32, uint, uint64, uint32:
			return value
		case string:
			truncated, _ := wire.TruncateUTF8(value, stringBytes, "…")
			return truncated
		}
		if depth >= 5 {
			return "[batch depth limit]"
		}
		switch value := candidate.(type) {
		case []any:
			out := make([]any, min(len(value), arrayItems))
			for index := range out {
				out[index] = trim(value[index], depth+1)
			}
			return out
		case map[string]any:
			keys := make([]string, 0, len(value))
			for key := range value {
				keys = append(keys, key)
			}
			sort.Slice(keys, func(left, right int) bool {
				a, aKnown := ranks[keys[left]]
				if !aKnown {
					a = 9999
				}
				b, bKnown := ranks[keys[right]]
				if !bKnown {
					b = 9999
				}
				if a != b {
					return a < b
				}
				return keys[left] < keys[right]
			})
			out := map[string]any{}
			for _, key := range keys[:min(len(keys), objectKeys)] {
				out[key] = trim(value[key], depth+1)
			}
			return out
		default:
			return candidate
		}
	}
	for attempt := 0; attempt < 6; attempt++ {
		candidate := trim(compact, 0)
		if jsonBytes(candidate) <= maxBytes {
			return candidate, true
		}
		stringBytes, arrayItems, objectKeys = max(32, stringBytes/2), max(1, arrayItems/2), max(4, objectKeys/2)
	}
	summary := map[string]any{"_batch_truncated": true}
	if object, ok := compact.(map[string]any); ok {
		for _, key := range wire.BatchPriority {
			value, present := object[key]
			if !present || isWorkUndefined(value) {
				continue
			}
			switch item := value.(type) {
			case []any, map[string]any:
				continue
			case string:
				value, _ = wire.TruncateUTF8(item, 128, "…")
			}
			summary[key] = value
			if jsonBytes(summary) > maxBytes {
				delete(summary, key)
				break
			}
		}
	}
	return summary, true
}
