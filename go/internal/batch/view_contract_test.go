package batch

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lyeith/codexpro/go/internal/wire"
)

func TestSavedBatchViewerAcceptsUnknownJSONWithoutExecutionOrMaintenance(t *testing.T) {
	var calls atomic.Int32
	service, _, workspace := fixture(t, func(context.Context, wire.Scope, string, map[string]any) (wire.Result, error) {
		calls.Add(1)
		return wire.Result{}, nil
	})
	directory := filepath.Join(workspace.Root, StoreDir)
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	// Above the twenty-recipe cap: a mistaken execution/maintenance path would
	// prune entries and/or touch the viewed file rather than simply render it.
	body := []byte(`{"version":2,"mode":"unknown-mode","continue_on_error":false,"operations":[{"id":"future","tool":"future-tool","args":{"command":"must never execute"}}]}`)
	stamp := time.Unix(1000000, 0)
	for index := 0; index < 24; index++ {
		path := filepath.Join(directory, fmt.Sprintf("%04X.json", index))
		if err := os.WriteFile(path, body, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	excludes := filepath.Join(workspace.Root, ".git", "info", "exclude")
	if err := os.MkdirAll(filepath.Dir(excludes), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(excludes, []byte("unchanged private excludes\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(excludes, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	viewed := filepath.Join(directory, "0000.json")
	before, err := os.Stat(viewed)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.ReadDefinition(context.Background(), workspace, StoreDir+"/0000.json")
	if err != nil {
		t.Fatal(err)
	}
	if result["autoStored"] != true || result["path"] != StoreDir+"/0000.json" {
		t.Fatalf("view attribution changed: %#v", result)
	}
	expected := any(nil)
	if err := json.Unmarshal(body, &expected); err != nil {
		t.Fatal(err)
	}
	actualJSON, _ := json.Marshal(result["definition"])
	expectedJSON, _ := json.Marshal(expected)
	if string(actualJSON) != string(expectedJSON) {
		t.Fatal("viewer changed arbitrary parsed JSON")
	}
	after, err := os.Stat(viewed)
	if err != nil {
		t.Fatal(err)
	}
	retained, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	current, err := os.ReadFile(viewed)
	if err != nil {
		t.Fatal(err)
	}
	excludeBody, err := os.ReadFile(excludes)
	if err != nil {
		t.Fatal(err)
	}
	excludeInfo, err := os.Stat(excludes)
	if err != nil {
		t.Fatal(err)
	}
	if len(retained) != 24 || !before.ModTime().Equal(after.ModTime()) || string(current) != string(body) ||
		string(excludeBody) != "unchanged private excludes\n" || !excludeInfo.ModTime().Equal(stamp) || calls.Load() != 0 {
		t.Fatal("viewer executed a child, pruned/touched a recipe, or modified Git excludes")
	}
	execution := invoke(t, service, map[string]any{"path": StoreDir + "/0000.json"})
	if !execution.IsError || errorCode(execution) != "batch_file_invalid" || calls.Load() != 0 {
		t.Fatal("loosening display parsing also loosened actual execution validation")
	}
}

func TestSavedBatchViewerPreservesJSONValuesAndReadGuards(t *testing.T) {
	service, _, workspace := fixture(t, func(context.Context, wire.Scope, string, map[string]any) (wire.Result, error) {
		t.Error("viewer called execution")
		return wire.Result{}, nil
	})
	for index, body := range []string{`null`, `42`, `"text"`, `[1,{"preserved":true}]`, `{"operations":[]}`} {
		name := fmt.Sprintf("value-%d.json", index)
		if err := os.WriteFile(filepath.Join(workspace.Root, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		view, err := service.ReadDefinition(context.Background(), workspace, name)
		if err != nil {
			t.Fatal(err)
		}
		got, err := json.Marshal(view["definition"])
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != body {
			t.Fatalf("raw value%d changed: %s", index, got)
		}
	}
	for name, body := range map[string][]byte{"broken.json": []byte("{"), "binary.json": []byte{'{', 0, '}'}, "plain.txt": []byte("{}")} {
		if err := os.WriteFile(filepath.Join(workspace.Root, name), body, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := service.ReadDefinition(context.Background(), workspace, name); err == nil {
			t.Fatalf("read guard accepted %s", name)
		}
	}
	if _, err := service.ReadDefinition(context.Background(), workspace, "../outside.json"); err == nil {
		t.Fatal("viewer accepted workspace escape")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.ReadDefinition(ctx, workspace, "value-0.json"); err == nil || !strings.Contains(err.Error(), "canceled") {
		t.Fatal("viewer ignored cancelled read context")
	}
}
