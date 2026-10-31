package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/lyeith/codexpro/go/internal/wire"
)

func TestAdmissionClonePreservesEmptyArraysAndNulls(t *testing.T) {
	value := Action{
		"empty_strings": []string{}, "null_strings": []string(nil),
		"empty_values": []any{}, "null_values": []any(nil),
		"empty_maps": []map[string]any{}, "null_maps": []map[string]any(nil),
		"empty_actions": []Action{}, "null_actions": []Action(nil),
		"empty_map": map[string]any{}, "null_map": map[string]any(nil),
		"null_action": Action(nil), "paths": []string{"first.txt"},
	}
	before, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	copy := cloneMetadata(value).(Action)
	after, err := json.Marshal(copy)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("admission copy changed JSON array/null presence: before=%s after=%s", before, after)
	}
	value["paths"].([]string)[0] = "caller-mutated.txt"
	if copy["paths"].([]string)[0] != "first.txt" {
		t.Fatal("admission copy retained caller-owned slice storage")
	}
}

func TestReadAndServerActionPersistEmptyPathArrays(t *testing.T) {
	clock := time.Date(2026, 10, 31, 18, 0, 0, 0, time.UTC)
	j := &Journal{options: Options{Clock: func() time.Time { return clock }, Redact: wire.Redact}, serverSessionRef: "srv_" + strings.Repeat("a", 32)}
	for _, tool := range []string{"read", "list_projects"} {
		args := map[string]any{}
		if tool == "read" {
			args["path"] = "fixture.txt"
		}
		action, err := j.build(RecordInput{ToolName: tool, Args: args, Scope: wire.Scope{PrincipalID: "owner", RequestID: tool}}, 1)
		if err != nil {
			t.Fatal(err)
		}
		frozen := cloneMetadata(action).(Action)
		encoded, err := serializeAction(frozen)
		if err != nil {
			t.Fatal(err)
		}
		var value map[string]any
		if err = json.Unmarshal(encoded, &value); err != nil {
			t.Fatal(err)
		}
		paths, ok := value["changed_paths"].([]any)
		if !ok || len(paths) != 0 {
			t.Fatalf("%s changed_paths must be [], not null: %v", tool, value["changed_paths"])
		}
		if tool == "list_projects" {
			paths, ok = value["targets"].([]any)
			if !ok || len(paths) != 0 {
				t.Fatalf("server target list must be []: %v", value["targets"])
			}
		}
	}
}

func TestEnabledAuditExportOmitsTextResultMetadata(t *testing.T) {
	j, err := Open(Options{Directory: t.TempDir(), Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	for _, format := range []string{"json", "jsonl"} {
		result, err := j.Export(context.Background(), Filter{Limit: 1}, format)
		if err != nil {
			t.Fatal(err)
		}
		if result.Meta != nil {
			t.Fatalf("%s export gained _meta absent from release raw envelope", format)
		}
	}
}

func TestAuditMetadataPathsMatchOriginalNodeGoldens(t *testing.T) {
	// Share the exact original-source oracle with the engine capture test so
	// display metadata and filesystem evidence cannot normalize differently.
	data, err := os.ReadFile("../engine/testdata/capture-paths.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		SourceCommit string `json:"source_commit"`
		Cases        []struct {
			Input any    `json:"input"`
			Safe  string `json:"safe"`
		} `json:"cases"`
	}
	if err = json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.SourceCommit != "44bb76bfa671e4aad540a8976f0fa6e6d5c2507a" || len(fixture.Cases) < 20 {
		t.Fatal("incomplete original-source path fixture")
	}
	for _, item := range fixture.Cases {
		if got := relative(item.Input); got != item.Safe {
			t.Errorf("audit path %q: got %q, want %q", item.Input, got, item.Safe)
		}
	}
}
