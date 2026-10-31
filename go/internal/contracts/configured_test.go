package contracts

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
)

type capabilityOracle struct {
	SchemaVersion int    `json:"schema_version"`
	SourceSHA256  string `json:"source_sha256"`
	Cases         []struct {
		Options ConfiguredOptions `json:"options"`
		Names   []string          `json:"source_tool_names"`
	} `json:"cases"`
}

func approvedRetirements(t *testing.T) map[string]bool {
	t.Helper()
	body, err := os.ReadFile("../../../docs/go-rewrite/APPROVAL.md")
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, line := range strings.Split(string(body), "\n") {
		columns := strings.Split(line, "|")
		if len(columns) == 5 && strings.TrimSpace(columns[3]) == "Retired" {
			names[strings.TrimSpace(columns[1])] = true
		}
	}
	if len(names) != 11 {
		t.Fatalf("approved retirement census changed: %v", names)
	}
	return names
}

// Expected inventories were evaluated by the reference toolNamesForMode itself,
// not a second copy of configuredAvailable. The source digest binds this oracle
// to the checked-in reference implementation; APPROVAL.md supplies retirements.
func TestConfiguredInventoryMatchesReferenceSourceOracle(t *testing.T) {
	body, err := os.ReadFile("testdata/registry-capability-oracle.json")
	if err != nil {
		t.Fatal(err)
	}
	var oracle capabilityOracle
	if err = json.Unmarshal(body, &oracle); err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile("../../../src/tools/registry.ts")
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(source)
	if oracle.SchemaVersion != 1 || oracle.SourceSHA256 != hex.EncodeToString(digest[:]) || len(oracle.Cases) != 144 {
		t.Fatal("capability oracle has incomplete profiles or no longer matches the reference source")
	}
	retired := approvedRetirements(t)
	profiles := referenceProfiles(t)
	for _, scenario := range oracle.Cases {
		name := fmt.Sprintf("%s/%s/projects%d/analysis%t/audit%s/test%t", scenario.Options.WorktreeMode,
			scenario.Options.ToolMode, scenario.Options.ProjectCount, scenario.Options.AnalysisEnabled,
			scenario.Options.AuditMode, scenario.Options.ConnectionTest)
		t.Run(name, func(t *testing.T) {
			allowed := map[string]bool{}
			for _, name := range scenario.Names {
				if !retired[name] {
					allowed[name] = true
				}
			}
			// The policy table's iteration order differs from actual TS
			// registration (notably Bash/work tools). Membership comes from
			// the source oracle; ordering comes from frozen tools/list evidence.
			profile := "direct-single-full"
			if scenario.Options.WorktreeMode == "mcp" {
				profile = "mcp-standard"
			}
			expected := []string{}
			for _, name := range profiles[profile].ToolNames {
				if allowed[name] {
					expected = append(expected, name)
				}
			}
			if len(expected) != len(allowed) {
				t.Fatalf("raw reference capture is incomplete for source capabilities: %v", allowed)
			}
			registry, err := NewConfigured(scenario.Options)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(registry.Names(), expected) {
				t.Fatalf("inventory differs from source oracle\ngot:  %v\nwant: %v", registry.Names(), expected)
			}
			base, err := NewVariant(scenario.Options.WorktreeMode, scenario.Options.ToolMode, scenario.Options.ProjectCount)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(registry.Initialization, base.Initialization) {
				t.Fatal("capability filtering rewrote frozen initialization")
			}
			for _, original := range base.Tools {
				tool, present := registry.Tool(original.Name)
				if present != allowed[original.Name] {
					t.Fatalf("dispatch lookup disagrees with source for %s", original.Name)
				}
				if present {
					if !bytes.Equal(tool.Raw, original.Raw) {
						t.Fatalf("filtering changed raw descriptor %s", original.Name)
					}
				} else {
					_, err := registry.ValidateInner(original.Name, map[string]any{})
					failure, ok := err.(*ValidationError)
					if !ok || !failure.Unknown {
						t.Fatalf("hidden tool remains reachable through batch validation: %s (%v)", original.Name, err)
					}
				}
			}
			for name := range retired {
				if _, present := registry.Tool(name); present {
					t.Fatalf("configured inventory restored approved retirement %s", name)
				}
			}
		})
	}
}

func TestConfiguredDefaultCapabilitiesPreserveLegacyConstructors(t *testing.T) {
	for _, mode := range []string{"", "off", "direct", "mcp"} {
		for _, tier := range []string{"", "minimal", "standard", "full"} {
			for _, projects := range []int{0, 1, 2} {
				base, err := NewVariant(mode, tier, projects)
				if err != nil {
					t.Fatal(err)
				}
				configured, err := NewConfigured(ConfiguredOptions{WorktreeMode: mode, ToolMode: tier,
					ProjectCount: projects, AnalysisEnabled: true})
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(base.List(), configured.List()) {
					t.Fatalf("default capabilities changed %s/%s/%d", mode, tier, projects)
				}
			}
		}
	}
	base, err := New()
	if err != nil {
		t.Fatal(err)
	}
	configured, err := NewConfigured(ConfiguredOptions{WorktreeMode: "off", ToolMode: "standard", ProjectCount: 2,
		AnalysisEnabled: true, AuditMode: "metadata"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(base.List(), configured.List()) {
		t.Fatal("New's frozen standard inventory changed")
	}
}

func TestConfiguredRejectsInvalidCapabilitySelectors(t *testing.T) {
	for _, options := range []ConfiguredOptions{
		{WorktreeMode: "unknown"}, {ToolMode: "unknown"}, {ProjectCount: -1}, {AuditMode: "unknown"},
	} {
		if _, err := NewConfigured(options); err == nil {
			t.Fatalf("invalid capability configuration accepted: %+v", options)
		}
	}
}
