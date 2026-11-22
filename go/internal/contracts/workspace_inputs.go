package contracts

import (
	"github.com/google/jsonschema-go/jsonschema"
)

type createProjectInput struct {
	ProjectID     string `json:"project_id" jsonschema:"Stable lowercase project id to add to the catalog."`
	ParentID      string `json:"parent_id" jsonschema:"Creation-root or project id returned by list_projects whose root will contain the new direct-child directory."`
	Label         string `json:"label,omitempty" jsonschema:"Human-readable project label. Default: project_id."`
	Directory     string `json:"directory,omitempty" jsonschema:"Portable direct-child directory name. Default: project_id. Path separators are not allowed."`
	Source        string `json:"source" jsonschema:"empty creates a raw directory; git initializes or clones a Git repository."`
	Repository    string `json:"repository,omitempty" jsonschema:"Optional HTTPS/SSH Git repository URL or allowed local path to clone when source=git."`
	InitialBranch string `json:"initial_branch,omitempty" jsonschema:"Initial branch for a newly initialized repository. Default: main. Cannot be combined with repository."`
	BaseRef       string `json:"base_ref,omitempty" jsonschema:"Optional Git ref stored with the project. It must resolve after Git initialization or clone."`
	MaxWorktrees  int    `json:"max_worktrees,omitempty" jsonschema:"Optional worktree count limit stored in the project catalog."`
	toolInput
}

type openCurrentWorkspaceInput struct {
	IncludeTree         bool `json:"include_tree,omitempty" jsonschema:"Include a compact file tree. Default: false for speed."`
	MaxDepth            int  `json:"max_depth,omitempty" jsonschema:"Tree depth when include_tree=true. Default: 2."`
	IncludeSkills       bool `json:"include_skills,omitempty" jsonschema:"Discover skills by name/description. Default: false for speed."`
	IncludeGlobalSkills bool `json:"include_global_skills,omitempty" jsonschema:"Also scan installed user/plugin skills when include_skills=true. Default: false."`
	toolInput
}

type openWorkspaceInput struct {
	WorkspaceID         string   `json:"workspace_id,omitempty" jsonschema:"A current workspace ID returned by list_projects or work_status. Loads its project instructions."`
	ProjectID           string   `json:"project_id,omitempty" jsonschema:"One project id from list_projects. Cannot be combined with project_ids."`
	ProjectIDs          []string `json:"project_ids,omitempty" jsonschema:"Open several projects in one call. Duplicate ids are collapsed; the first project becomes the selected primary workspace. Cannot be combined with project_id."`
	IncludeTree         bool     `json:"include_tree,omitempty" jsonschema:"Include compact file trees. Defaults to true for a newly opened singular workspace, false for repeated singular opens and project_ids arrays."`
	MaxDepth            int      `json:"max_depth,omitempty" jsonschema:"Tree depth. Default: 3."`
	MaxEntries          int      `json:"max_entries,omitempty" jsonschema:"Maximum tree entries. Default: 500. With project_ids, this is one total budget divided across the opened workspaces."`
	IncludeSkills       bool     `json:"include_skills,omitempty" jsonschema:"Discover skills by name/description. Default: false for speed."`
	IncludeGlobalSkills bool     `json:"include_global_skills,omitempty" jsonschema:"Also scan installed user/plugin skills when include_skills=true. Default: false."`
	BootstrapContext    bool     `json:"bootstrap_context,omitempty" jsonschema:"Deprecated and ignored. Opening a workspace does not create context files."`
	toolInput
}

func listProjectsDefinition() definition {
	return defineTool[toolInput]("list_projects", "List Projects", readOnlyTool,
		"List the configured projects with their ids and workspace_ids, plus creation roots for "+
			"create_project. Call this first. The returned workspace_id can be passed straight to "+
			"tree/search/read for read-only work; call open_workspace(project_id) before editing to load "+
			"AGENTS.md guidance. Only ids returned here are valid.", nil)
}

func createProjectDefinition() definition {
	return defineTool[createProjectInput]("create_project", "Create Project", createProjectTool,
		"Create a new project as a direct child of a named creation root or existing project, persist it in "+
			"the projects catalog, and register it immediately. Prefer creation roots to avoid nesting "+
			"repositories. source=empty creates a raw directory in direct mode. source=git either initializes a "+
			"repository with an initial commit or clones repository when provided.", func(s *jsonschema.Schema) {
			s.Properties["project_id"].Pattern = "^[a-z0-9][a-z0-9._-]{0,63}$"
			s.Properties["parent_id"].MinLength = new(1)
			s.Properties["label"].MaxLength = new(120)
			s.Properties["directory"].MaxLength = new(120)
			allowValues(s.Properties["source"], "empty", "git")
			s.Properties["repository"].MaxLength = new(2048)
			s.Properties["initial_branch"].MaxLength = new(255)
			s.Properties["base_ref"].MaxLength = new(256)
			integerRange(s.Properties["max_worktrees"], 1, 512)
		})
}

func listWorkspacesDefinition() definition {
	return defineTool[toolInput]("list_workspaces", "List Workspaces", readOnlyTool,
		"List workspaces opened in this MCP session and identify the currently selected workspace.", nil)
}

func openCurrentWorkspaceDefinition() definition {
	return defineTool[openCurrentWorkspaceInput]("open_current_workspace", "Open Current Workspace", workspaceOpenTool,
		"Open and select the configured default workspace for this MCP session. Use this to return to the "+
			"launch workspace after switching roots.", func(s *jsonschema.Schema) {
			integerRange(s.Properties["max_depth"], 1, 8)
		})
}

func openWorkspaceDefinition() definition {
	return defineTool[openWorkspaceInput]("open_workspace", "Open Workspace", workspaceOpenTool,
		"Open a catalog project by project_id (or several with project_ids) and return its workspace_id, "+
			"AGENTS.md guidance, git status and an optional tree. Only ids returned by list_projects are valid; "+
			"an unknown id fails and lists the valid ones. Not required before read-only tools (list_projects "+
			"already gives workspace_ids), but call it once before editing. Reuse workspace_ids instead of "+
			"reopening.", func(s *jsonschema.Schema) {
			s.Properties["project_id"].MinLength = new(1)
			arrayRange(s.Properties["project_ids"], 1, 12)
			s.Properties["project_ids"].Items.MinLength = new(1)
			integerRange(s.Properties["max_depth"], 1, 8)
			integerRange(s.Properties["max_entries"], 1, 3000)
		})
}
