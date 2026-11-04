package contracts

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestReadDiscoveryContract(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	trusted, err := json.Marshal(r.List())
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(r.ReadList())
	if err != nil {
		t.Fatal(err)
	}
	var list struct {
		Tools []struct {
			Name        string
			InputSchema struct {
				Properties           map[string]struct{ Enum []string }
				Required             []string
				AdditionalProperties *bool
			}
		}
	}
	if err := json.Unmarshal(b, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Tools) != 7 || strings.Contains(string(b), "open_workspace") {
		t.Fatal("unexpected read discovery instructions")
	}
	for _, tool := range list.Tools {
		s := tool.InputSchema
		if s.AdditionalProperties == nil || *s.AdditionalProperties {
			t.Fatalf("%s advertises unknown inputs", tool.Name)
		}
		switch tool.Name {
		case "list_projects":
			if len(s.Required) != 0 || len(s.Properties) != 1 {
				t.Fatal("project discovery inputs changed")
			}
		case "activity_list":
			if !slices.Equal(s.Required, []string{"project_id"}) {
				t.Fatal("activity_list lacks required project")
			}
		case "activity_get":
			if !slices.Equal(s.Required, []string{"action_id"}) {
				t.Fatal("activity_get requires more than receipt ID")
			}
		default:
			if !slices.Contains(s.Required, "workspace_id") {
				t.Fatalf("%s lacks required workspace", tool.Name)
			}
		}
		if tool.Name == "search" {
			expect := []string{"agent", "workspace_id", "path", "query", "kind", "scope", "max_results", "context_before", "context_after", "cursor"}
			if len(s.Properties) != len(expect) || !slices.Contains(s.Required, "query") {
				t.Fatal("unexpected read search options")
			}
			for _, k := range expect {
				if _, ok := s.Properties[k]; !ok {
					t.Fatalf("missing search option %s", k)
				}
			}
			if !slices.Equal(s.Properties["kind"].Enum, []string{"text"}) || !slices.Equal(s.Properties["scope"].Enum, []string{"workspace"}) {
				t.Fatal("search advertises unsupported modes")
			}
		}
	}
	after, err := json.Marshal(r.List())
	if err != nil || !bytes.Equal(trusted, after) || !bytes.Contains(after, []byte(`"base_ref"`)) {
		t.Fatal("trusted discovery changed")
	}
}
