package contracts

import (
	"reflect"
	"testing"
)

func TestConfiguredInventory(t *testing.T) {
	for _, scenario := range []struct {
		name    string
		options ConfiguredOptions
		present []string
		absent  []string
	}{
		{"standard", ConfiguredOptions{ProjectCount: 2, AnalysisEnabled: true},
			[]string{"list_projects", "read", "search", "inspect_workspace", "work_status", "activity_list", "queue_jobs"},
			[]string{"open_current_workspace", "list_workspaces", "create_workspace", "release_workspace", "remove_workspace"}},
		{"minimal single", ConfiguredOptions{ToolMode: "minimal", ProjectCount: 1, AnalysisEnabled: true},
			[]string{"list_projects", "read", "open_current_workspace", "work_status", "activity_list", "queue_jobs"},
			[]string{"search", "inspect_workspace", "list_workspaces", "create_workspace"}},
		{"full", ConfiguredOptions{ToolMode: "full", ProjectCount: 1},
			[]string{"read", "list_workspaces", "open_current_workspace", "search"},
			[]string{"inspect_workspace", "create_workspace"}},
		{"audit off", ConfiguredOptions{ProjectCount: 1, AuditMode: "off", AnalysisEnabled: true},
			[]string{"read", "queue_jobs"},
			[]string{"activity_list", "activity_get", "activity_status", "activity_export"}},
		{"connection test", ConfiguredOptions{ProjectCount: 1, ConnectionTest: true},
			[]string{"read", "jobs", "open_workspace", "activity_get"},
			[]string{"list_projects", "create_project", "codexpro_self_test", "start_jobs", "queue_jobs", "stop_jobs", "write", "edit", "apply_patch", "import_file", "bash", "commit_changes"}},
		{"multi-project connection test", ConfiguredOptions{ProjectCount: 2, ConnectionTest: true},
			[]string{"list_projects", "read", "open_workspace"},
			[]string{"queue_jobs", "write", "bash", "inspect_workspace"}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			r, err := NewConfigured(scenario.options)
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range scenario.present {
				if _, ok := r.Tool(name); !ok {
					t.Errorf("missing configured tool %s", name)
				}
			}
			for _, name := range append(scenario.absent, "work_manage", "work_update", "codexpro_inventory") {
				if _, ok := r.Tool(name); ok {
					t.Errorf("tool %s remains available", name)
				}
				if _, err := r.ValidateInner(name, nil); err == nil {
					t.Errorf("disabled tool %s is callable by batch", name)
				}
			}
		})
	}
	defaults, err := New()
	if err != nil {
		t.Fatal(err)
	}
	variant, err := NewConfigured(ConfiguredOptions{ToolMode: "standard", ProjectCount: 2, AnalysisEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(defaults.List(), variant.List()) {
		t.Fatal("default tool inventory changed")
	}
}

func TestConfiguredRejectsInvalidCapabilitySelectors(t *testing.T) {
	for _, options := range []ConfiguredOptions{
		{ToolMode: "unknown"}, {ProjectCount: -1}, {AuditMode: "unknown"},
	} {
		if _, err := NewConfigured(options); err == nil {
			t.Errorf("invalid capability configuration accepted: %+v", options)
		}
	}
}
