package contracts

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

// Only workspace lifecycle descriptors and verified workspace-routing text are
// imported from the conditional reference. Its mcp profile used safe Bash to
// satisfy the old startup guard; all other full-permission fields and the
// initialization remain unchanged. The derived raw overrides have a field-level
// provenance record and are checked against both complete reference captures.
//
//go:embed assets/conditional-workspaces.json
var conditionalWorkspacesJSON []byte

type conditionalProfile struct {
	Profile              string                     `json:"profile"`
	ToolNames            []string                   `json:"tool_names"`
	WorkspaceDescriptors map[string]json.RawMessage `json:"workspace_descriptors"`
}

// NewVariant applies the retained source inventory tiers to the frozen full
// permission descriptors. New retains its original multi-project standard
// inventory of 28 tools; callers with runtime catalog information use this API.
func NewVariant(worktreeMode, toolMode string, projectCount int) (*Registry, error) {
	if worktreeMode == "" || worktreeMode == "off" {
		worktreeMode = "direct"
	}
	if worktreeMode != "direct" && worktreeMode != "mcp" {
		return nil, fmt.Errorf("unknown worktree mode: %s", worktreeMode)
	}
	if toolMode == "" {
		toolMode = "standard"
	}
	if toolMode != "minimal" && toolMode != "standard" && toolMode != "full" {
		return nil, fmt.Errorf("unknown tool mode: %s", toolMode)
	}
	if projectCount < 0 {
		return nil, fmt.Errorf("project count cannot be negative")
	}
	var reference struct {
		Captures              []conditionalProfile       `json:"captures"`
		MCPRoutingDescriptors map[string]json.RawMessage `json:"mcp_full_routing_descriptors"`
	}
	if err := json.Unmarshal(conditionalWorkspacesJSON, &reference); err != nil {
		return nil, err
	}
	profileName := "direct-single-full"
	if worktreeMode == "mcp" {
		profileName = "mcp-standard"
	}
	var profile *conditionalProfile
	for i := range reference.Captures {
		if reference.Captures[i].Profile == profileName {
			if profile != nil {
				return nil, fmt.Errorf("duplicate conditional profile: %s", profileName)
			}
			profile = &reference.Captures[i]
		}
	}
	if profile == nil {
		return nil, fmt.Errorf("missing conditional profile: %s", profileName)
	}
	var base []json.RawMessage
	if err := json.Unmarshal(toolsJSON, &base); err != nil {
		return nil, err
	}
	byName := map[string]json.RawMessage{}
	for _, raw := range base {
		var header struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(raw, &header); err != nil {
			return nil, err
		}
		if _, exists := byName[header.Name]; exists {
			return nil, fmt.Errorf("duplicate base descriptor: %s", header.Name)
		}
		byName[header.Name] = raw
	}
	ordered := []json.RawMessage{}
	seen := map[string]bool{}
	for _, name := range profile.ToolNames {
		if seen[name] {
			return nil, fmt.Errorf("duplicate conditional tool name: %s", name)
		}
		seen[name] = true
		if retiredDescriptor(name) || !variantAvailable(name, worktreeMode, toolMode, projectCount) {
			continue
		}
		raw, ok := byName[name]
		if !ok || worktreeMode == "mcp" && name == "open_workspace" {
			raw, ok = profile.WorkspaceDescriptors[name]
		}
		if worktreeMode == "mcp" {
			if routed, exists := reference.MCPRoutingDescriptors[name]; exists {
				raw, ok = routed, true
			}
		}
		if !ok {
			return nil, fmt.Errorf("retained conditional tool has no frozen descriptor: %s", name)
		}
		var header struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(raw, &header); err != nil {
			return nil, err
		}
		if header.Name != name {
			return nil, fmt.Errorf("conditional descriptor name mismatch: %s", name)
		}
		ordered = append(ordered, raw)
	}
	for name := range byName {
		if !retiredDescriptor(name) && variantAvailable(name, worktreeMode, toolMode, projectCount) && !seen[name] {
			return nil, fmt.Errorf("conditional profile omitted retained tool: %s", name)
		}
	}
	return newRegistry(ordered)
}

// Source: src/tools/registry.ts117-316 at 44bb76b. Full permission, audit,
// analysis and a persistent project catalog are fixed retained capabilities.
func variantAvailable(name, worktreeMode, toolMode string, projectCount int) bool {
	switch name {
	case "load_skill", "inspect_workspace", "tree", "search", "ast_grep", "view_image":
		return toolMode != "minimal"
	case "create_workspace", "release_workspace", "remove_workspace":
		return worktreeMode == "mcp"
	case "list_workspaces":
		return worktreeMode != "mcp" && toolMode == "full"
	case "open_current_workspace":
		return worktreeMode != "mcp" && projectCount <= 1
	default:
		return true
	}
}

// The approved descriptor removals in docs/go-rewrite/APPROVAL.md are applied
// to every inventory path, including any future capture containing opt-ins.
func retiredDescriptor(name string) bool {
	switch name {
	case "work_manage", "work_update", "codexpro", "codexpro_inventory",
		"read_handoff", "wait_for_handoff", "codex_context", "export_pro_context",
		"handoff_to_agent", "codex_sessions", "read_codex_session":
		return true
	default:
		return false
	}
}
