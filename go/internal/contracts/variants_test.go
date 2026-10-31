package contracts

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

func referenceProfiles(t *testing.T) map[string]conditionalProfile {
	t.Helper()
	b, err := os.ReadFile("../../../docs/go-rewrite/evidence/conditional-workspace-schemas.json")
	if err != nil {
		t.Fatal(err)
	}
	var source struct {
		Captures []conditionalProfile `json:"captures"`
	}
	if err = json.Unmarshal(b, &source); err != nil {
		t.Fatal(err)
	}
	profiles := map[string]conditionalProfile{}
	for _, profile := range source.Captures {
		profiles[profile.Profile] = profile
	}
	if len(profiles) != 2 {
		t.Fatalf("missing conditional reference capture: %v", profiles)
	}
	return profiles
}

func equalRawDescriptor(t *testing.T, name string, actual, reference json.RawMessage) {
	t.Helper()
	var a, b bytes.Buffer
	if err := json.Compact(&a, actual); err != nil {
		t.Fatal(err)
	}
	if err := json.Compact(&b, reference); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a.Bytes(), b.Bytes()) {
		t.Fatalf("raw descriptor drift for %s\nactual: %s\nreference: %s", name, a.Bytes(), b.Bytes())
	}
}

func referenceTools(t *testing.T, profile string) map[string]json.RawMessage {
	t.Helper()
	b, err := os.ReadFile("../../../docs/go-rewrite/evidence/conditional-workspace-schemas.json")
	if err != nil {
		t.Fatal(err)
	}
	var source struct {
		Captures []struct {
			Profile string `json:"profile"`
			Pages   []struct {
				Body struct {
					Result struct {
						Tools []json.RawMessage `json:"tools"`
					} `json:"result"`
				} `json:"body"`
			} `json:"tools_list_pages"`
		} `json:"captures"`
	}
	if err = json.Unmarshal(b, &source); err != nil {
		t.Fatal(err)
	}
	tools := map[string]json.RawMessage{}
	for _, capture := range source.Captures {
		if capture.Profile != profile {
			continue
		}
		for _, page := range capture.Pages {
			for _, raw := range page.Body.Result.Tools {
				var tool Tool
				if err = json.Unmarshal(raw, &tool); err != nil {
					t.Fatal(err)
				}
				tools[tool.Name] = raw
			}
		}
	}
	if len(tools) == 0 {
		t.Fatalf("no captured tools for %s", profile)
	}
	return tools
}

func TestConditionalDescriptorsEqualRawCapture(t *testing.T) {
	profiles := referenceProfiles(t)
	base, err := New()
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []struct {
		profile, worktree, tier string
		projects                int
	}{
		{"mcp-standard", "mcp", "standard", 1},
		{"direct-single-full", "off", "full", 1},
	} {
		t.Run(scenario.profile, func(t *testing.T) {
			r, err := NewVariant(scenario.worktree, scenario.tier, scenario.projects)
			if err != nil {
				t.Fatal(err)
			}
			source := profiles[scenario.profile]
			sourceTools := referenceTools(t, scenario.profile)
			expected := []string{}
			for _, name := range source.ToolNames {
				if name != "work_manage" && name != "work_update" && name != "codexpro" && name != "codexpro_inventory" {
					expected = append(expected, name)
				}
			}
			if !reflect.DeepEqual(r.Names(), expected) {
				t.Fatalf("conditional registration order drifted: %v != %v", r.Names(), expected)
			}
			for name, raw := range source.WorkspaceDescriptors {
				tool, ok := r.Tool(name)
				if !ok {
					t.Fatalf("missing conditional tool: %s", name)
				}
				equalRawDescriptor(t, name, tool.Raw, raw)
			}
			for _, tool := range base.Tools {
				if tool.Name == "open_workspace" {
					continue
				}
				actual, ok := r.Tool(tool.Name)
				if !ok {
					t.Fatalf("conditional inventory lost retained tool %s", tool.Name)
				}
				if scenario.worktree != "mcp" {
					equalRawDescriptor(t, tool.Name, actual.Raw, tool.Raw)
				} else if tool.Name == "bash" {
					// The only permission delta is the full Bash description. The
					// complete remaining descriptor must match the managed reference.
					expected := decodeJSON(sourceTools[tool.Name]).(map[string]any)
					expected["description"] = tool.Description
					if !reflect.DeepEqual(decodeJSON(actual.Raw), expected) {
						t.Fatal("MCP Bash changed beyond full-permission description")
					}
				} else {
					equalRawDescriptor(t, tool.Name, actual.Raw, sourceTools[tool.Name])
				}
			}
			if !reflect.DeepEqual(r.Initialization, base.Initialization) {
				t.Fatal("safe reference initialization was imported into full-permission runtime")
			}
		})
	}
}

func TestMCPRoutingChangesOnlyNineteenCapturedDescriptions(t *testing.T) {
	base, err := New()
	if err != nil {
		t.Fatal(err)
	}
	managed, err := NewVariant("mcp", "standard", 1)
	if err != nil {
		t.Fatal(err)
	}
	source := referenceTools(t, "mcp-standard")
	changed := 0
	for _, tool := range base.Tools {
		if tool.Name == "open_workspace" {
			continue
		}
		expected := decodeJSON(tool.Raw).(map[string]any)
		reference := decodeJSON(source[tool.Name]).(map[string]any)
		props := expected["inputSchema"].(map[string]any)["properties"].(map[string]any)
		refProps := reference["inputSchema"].(map[string]any)["properties"].(map[string]any)
		if field, ok := props["workspace_id"].(map[string]any); ok {
			refField := refProps["workspace_id"].(map[string]any)
			if field["description"] != refField["description"] {
				field["description"] = refField["description"]
				changed++
			}
		}
		actual, ok := managed.Tool(tool.Name)
		if !ok || !reflect.DeepEqual(decodeJSON(actual.Raw), expected) {
			t.Fatalf("%s changed beyond its verified routing description", tool.Name)
		}
	}
	if changed != 19 {
		t.Fatalf("expected exact19 routing text changes; got %d", changed)
	}
}

func TestConditionalAvailabilityMatrix(t *testing.T) {
	for _, scenario := range []struct {
		worktree, tier                 string
		projects, count                int
		current, list, managed, search bool
	}{
		{"off", "minimal", 1, 23, true, false, false, false},
		{"off", "minimal", 2, 22, false, false, false, false},
		{"off", "standard", 1, 29, true, false, false, true},
		{"off", "standard", 2, 28, false, false, false, true},
		{"direct", "full", 1, 30, true, true, false, true},
		{"direct", "full", 2, 29, false, true, false, true},
		{"mcp", "minimal", 1, 25, false, false, true, false},
		{"mcp", "minimal", 2, 25, false, false, true, false},
		{"mcp", "standard", 1, 31, false, false, true, true},
		{"mcp", "standard", 2, 31, false, false, true, true},
		{"mcp", "full", 1, 31, false, false, true, true},
		{"mcp", "full", 2, 31, false, false, true, true},
	} {
		r, err := NewVariant(scenario.worktree, scenario.tier, scenario.projects)
		if err != nil {
			t.Fatal(err)
		}
		if len(r.Tools) != scenario.count {
			t.Fatalf("%+v: got %d tools", scenario, len(r.Tools))
		}
		for name, want := range map[string]bool{"open_current_workspace": scenario.current, "list_workspaces": scenario.list, "create_workspace": scenario.managed, "release_workspace": scenario.managed, "remove_workspace": scenario.managed, "load_skill": scenario.search, "inspect_workspace": scenario.search, "tree": scenario.search, "search": scenario.search, "ast_grep": scenario.search, "view_image": scenario.search} {
			if _, present := r.Tool(name); present != want {
				t.Fatalf("%+v: %s presence=%v", scenario, name, present)
			}
		}
		for _, name := range []string{"work_status", "read", "write", "edit", "apply_patch", "bash", "start_jobs", "jobs", "stop_jobs", "batch", "import_file", "activity_export"} {
			if _, present := r.Tool(name); !present {
				t.Fatalf("%+v: required tool missing: %s", scenario, name)
			}
		}
	}
	base, _ := New()
	standard, err := NewVariant("off", "standard", 2)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(base.List(), standard.List()) {
		t.Fatal("default standard inventory changed")
	}
}

func TestConditionalValidationUsesCorrectOpenSchema(t *testing.T) {
	mcp, err := NewVariant("mcp", "standard", 1)
	if err != nil {
		t.Fatal(err)
	}
	direct, err := NewVariant("off", "standard", 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = direct.Validate("open_workspace", map[string]any{"project_ids": []any{"a", "b"}}); err != nil {
		t.Fatalf("direct project selection rejected: %v", err)
	}
	if _, err = mcp.Validate("open_workspace", map[string]any{"project_ids": []any{"a", "b"}}); err == nil {
		t.Fatal("MCP open accepted a project selector without retained workspace identity")
	}
	if _, err = mcp.Validate("open_workspace", map[string]any{"workspace_id": "wt_retained"}); err != nil {
		t.Fatalf("MCP retained workspace rejected: %v", err)
	}
	if _, err = mcp.Validate("create_workspace", map[string]any{}); err == nil {
		t.Fatal("persistent catalog create omitted required project_id")
	}
	for _, name := range []string{"release_workspace", "remove_workspace"} {
		if _, err = mcp.Validate(name, map[string]any{}); err == nil {
			t.Fatalf("%s omitted required workspace_id", name)
		}
		if _, err = direct.Validate(name, map[string]any{"workspace_id": "wt_retained"}); err == nil {
			t.Fatalf("%s became available in direct mode", name)
		}
	}
}

func TestEveryApprovedRetirementIsFilteredFromAllPaths(t *testing.T) {
	b, err := os.ReadFile("../../../docs/go-rewrite/APPROVAL.md")
	if err != nil {
		t.Fatal(err)
	}
	retired := []string{}
	for _, line := range strings.Split(string(b), "\n") {
		parts := strings.Split(line, "|")
		if len(parts) == 5 && strings.TrimSpace(parts[3]) == "Retired" {
			retired = append(retired, strings.TrimSpace(parts[1]))
		}
	}
	if len(retired) != 11 {
		t.Fatalf("approval retirement list changed: %v", retired)
	}
	raw := []json.RawMessage{}
	for _, name := range retired {
		if !retiredDescriptor(name) {
			t.Fatalf("approved retirement missing: %s", name)
		}
		b, _ := json.Marshal(map[string]any{"name": name, "inputSchema": map[string]any{"type": "object"}})
		raw = append(raw, b)
	}
	base, _ := New()
	read, _ := base.Tool("read")
	raw = append(raw, read.Raw)
	r, err := newRegistry(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(r.Names(), []string{"read"}) {
		t.Fatalf("retired descriptor leaked through frozen registry: %v", r.Names())
	}
	for _, mode := range []string{"off", "mcp"} {
		for _, tier := range []string{"minimal", "standard", "full"} {
			r, err := NewVariant(mode, tier, 1)
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range retired {
				if _, ok := r.Tool(name); ok {
					t.Fatalf("%s/%s leaked %s", mode, tier, name)
				}
			}
		}
	}
}

func TestVariantRejectsInvalidSelectors(t *testing.T) {
	for _, scenario := range []struct {
		mode, tier string
		count      int
	}{{"unknown", "standard", 1}, {"off", "unknown", 1}, {"off", "standard", -1}} {
		if _, err := NewVariant(scenario.mode, scenario.tier, scenario.count); err == nil {
			t.Fatalf("invalid variant accepted: %+v", scenario)
		}
	}
}
