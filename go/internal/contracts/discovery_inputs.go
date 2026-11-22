package contracts

import (
	"github.com/google/jsonschema-go/jsonschema"
)

type codexproSelfTestInput struct {
	workspaceInput
	WriteProbe          bool `json:"write_probe,omitempty" jsonschema:"Create/edit only .ai-bridge/codexpro-self-test.md. Default: false."`
	BashProbe           bool `json:"bash_probe,omitempty" jsonschema:"Check bash policy with safe local commands only. Default: false."`
	ProContextProbe     bool `json:"pro_context_probe,omitempty" jsonschema:"Build a selected-only Pro context bundle in memory without writing pro-context.md. Default: false."`
	IncludeGlobalSkills bool `json:"include_global_skills,omitempty" jsonschema:"Include user/plugin skill discovery in the inventory check. Default: true."`
	MaxSkills           int  `json:"max_skills,omitempty" jsonschema:"Maximum skills to inspect during the inventory check. Default: 40."`
	toolInput
}

type loadSkillInput struct {
	workspaceInput
	Name                string `json:"name" jsonschema:"Exact skill name from the returned skill_inventory."`
	Source              string `json:"source,omitempty" jsonschema:"Optional source override. Without it, the highest-precedence skill is loaded."`
	Path                string `json:"path,omitempty" jsonschema:"Optional exact sanitized path override for diagnostics or an explicitly selected suppressed duplicate."`
	IncludeGlobalSkills bool   `json:"include_global_skills,omitempty" jsonschema:"Also scan installed user/plugin skills. Default: auto when source/path is not workspace."`
	MaxSkills           int    `json:"max_skills,omitempty" jsonschema:"Maximum skills to scan while resolving the requested skill. Default: 500."`
	MaxBytes            int    `json:"max_bytes,omitempty" jsonschema:"Maximum bytes to return from SKILL.md. Default: 40000."`
	toolInput
}

type inspectWorkspaceInput struct {
	workspaceInput
	Path                 string `json:"path,omitempty" jsonschema:"Optional workspace-relative area to emphasize. Default: entire workspace."`
	MaxFiles             int    `json:"max_files,omitempty" jsonschema:"Maximum returned file records. Default: 300."`
	IncludeSymbols       bool   `json:"include_symbols,omitempty" jsonschema:"Include symbols in structured output. Default: true."`
	IncludeRelationships bool   `json:"include_relationships,omitempty" jsonschema:"Include relationships in structured output. Default: true."`
	MaxSymbols           int    `json:"max_symbols,omitempty" jsonschema:"Maximum returned symbols. Analysis remains bounded by server config."`
	MaxRelationships     int    `json:"max_relationships,omitempty" jsonschema:"Maximum returned relationships. Analysis remains bounded by server config."`
	toolInput
}

type treeInput struct {
	workspaceInput
	Path          string `json:"path,omitempty" jsonschema:"Directory relative to workspace root. Default: ."`
	MaxDepth      int    `json:"max_depth,omitempty" jsonschema:"Maximum depth. Default: 4."`
	IncludeHidden bool   `json:"include_hidden,omitempty" jsonschema:"Include dotfiles/dotfolders that are not blocked. Default: false."`
	MaxEntries    int    `json:"max_entries,omitempty" jsonschema:"Maximum entries. Default: 800."`
	toolInput
}

type searchInput struct {
	workspaceInput
	Query            string `json:"query" jsonschema:"Text/regex to find, or a dotted/JSON-pointer configuration path when kind=config."`
	Kind             string `json:"kind,omitempty" jsonschema:"Text search or structured configuration-path query. Default: text."`
	Regex            bool   `json:"regex,omitempty" jsonschema:"Treat a text query as a regular expression. Default: false. Not valid for kind=config."`
	Path             string `json:"path,omitempty" jsonschema:"Directory or file relative to workspace root. Default: ."`
	Glob             string `json:"glob,omitempty" jsonschema:"Optional glob, for example src/**/*.ts or **/*.yaml."`
	IncludeHidden    bool   `json:"include_hidden,omitempty" jsonschema:"Include hidden files that are not blocked. Default: false."`
	MaxResults       int    `json:"max_results,omitempty" jsonschema:"Maximum matches in this page. Default from config."`
	ContextBefore    int    `json:"context_before,omitempty" jsonschema:"Lines before each match. Default: 2."`
	ContextAfter     int    `json:"context_after,omitempty" jsonschema:"Lines after each match. Default: 2."`
	GroupByFile      bool   `json:"group_by_file,omitempty" jsonschema:"Merge overlapping context ranges in each file. Default: true."`
	Cursor           string `json:"cursor,omitempty" jsonschema:"Opaque next_cursor from the previous page. All other search options must remain identical."`
	Scope            string `json:"scope,omitempty" jsonschema:"Search the workspace, current changed files, added diff lines, or removed diff lines. Default: workspace."`
	BaseRef          string `json:"base_ref,omitempty" jsonschema:"Git base ref for non-workspace scopes. Default: HEAD."`
	DiffTarget       string `json:"diff_target,omitempty" jsonschema:"Compare base_ref to the working tree, index, or HEAD. Default: worktree."`
	IncludeUntracked bool   `json:"include_untracked,omitempty" jsonschema:"Include untracked files for worktree changed_files/diff_added searches. Default: true."`
	ConfigFormat     string `json:"config_format,omitempty" jsonschema:"Configuration format. Default: infer from extension."`
	Intent           string `json:"intent,omitempty" jsonschema:"Optional structured repository-analysis intent for first-page workspace text searches."`
	Symbol           string `json:"symbol,omitempty" jsonschema:"Optional symbol query. Uses repository analysis and overrides query text."`
	IncludeTests     bool   `json:"include_tests,omitempty" jsonschema:"Include related tests in structured repository-analysis results. Default: false."`
	toolInput
}

type astGrepInput struct {
	workspaceInput
	Pattern       string   `json:"pattern,omitempty" jsonschema:"ast-grep structural pattern, for example console.log($ARG). Provide exactly one of pattern or kind."`
	Kind          string   `json:"kind,omitempty" jsonschema:"Tree-sitter node kind, for example function_declaration. Provide exactly one of pattern or kind."`
	Language      string   `json:"language,omitempty" jsonschema:"Optional ast-grep language id such as ts, tsx, js, py, go, rust, or java. Omit to infer from file extensions."`
	Selector      string   `json:"selector,omitempty" jsonschema:"Optional sub-node kind to return from a pattern match. Pattern mode only."`
	Strictness    string   `json:"strictness,omitempty" jsonschema:"Pattern matching strictness. Pattern mode only; ast-grep defaults to smart."`
	Path          string   `json:"path,omitempty" jsonschema:"Directory or file relative to the workspace root. Default: ."`
	Globs         []string `json:"globs,omitempty" jsonschema:"Optional ast-grep include/exclude globs. Prefix an exclusion with !. Safety-blocked paths remain excluded."`
	IncludeHidden bool     `json:"include_hidden,omitempty" jsonschema:"Include hidden files that are not blocked. Default: false."`
	MaxResults    int      `json:"max_results,omitempty" jsonschema:"Maximum structural matches in this page. Default from config."`
	ContextBefore int      `json:"context_before,omitempty" jsonschema:"Lines before each structural match. Default: 2."`
	ContextAfter  int      `json:"context_after,omitempty" jsonschema:"Lines after each structural match. Default: 2."`
	GroupByFile   bool     `json:"group_by_file,omitempty" jsonschema:"Merge overlapping structural context ranges in each file. Default: true."`
	Cursor        string   `json:"cursor,omitempty" jsonschema:"Opaque next_cursor from the previous page. Every other query option must remain identical."`
	TimeoutMS     int      `json:"timeout_ms,omitempty" jsonschema:"Native ast-grep process timeout. Default: 15000 ms."`
	toolInput
}

func serverConfigDefinition() definition {
	return defineTool[toolInput]("server_config", "Server Config", readOnlyTool,
		"Show CodexPro server configuration, safety modes, limits, and blocked paths. Does not reveal auth "+
			"tokens.", nil)
}

func codexproSelfTestDefinition() definition {
	return defineTool[codexproSelfTestInput]("codexpro_self_test", "CodexPro Self Test", localDestructiveTool,
		"Diagnostic only: checks modes, the registered tool set, workspace access, skills and git without "+
			"touching the repository. Optional probes (off by default) write one .ai-bridge file, run safe bash "+
			"commands, or build a Pro context bundle in memory. Not part of normal coding work.", func(s *jsonschema.Schema) {
			integerRange(s.Properties["max_skills"], 1, 120)
		})
}

func loadSkillDefinition() definition {
	return defineTool[loadSkillInput]("load_skill", "Load Skill", readOnlyTool,
		"Load the bounded SKILL.md body for a discovered workspace, user, or plugin skill by name. Does not "+
			"accept arbitrary paths; use after opening the workspace with include_skills=true returns "+
			"skill_inventory.", func(s *jsonschema.Schema) {
			allowValues(s.Properties["source"], "workspace", "user", "plugin", "other")
			integerRange(s.Properties["max_skills"], 1, 500)
			integerRange(s.Properties["max_bytes"], 1000, 100000)
		})
}

func inspectWorkspaceDefinition() definition {
	return defineTool[inspectWorkspaceInput]("inspect_workspace", "Inspect Workspace", readOnlyTool,
		"Build a bounded repository map with languages, project types, entrypoints, areas, symbols, "+
			"relationships, and coverage warnings.", func(s *jsonschema.Schema) {
			integerRange(s.Properties["max_files"], 1, 100000)
			integerRange(s.Properties["max_symbols"], 1, 100000)
			integerRange(s.Properties["max_relationships"], 1, 250000)
		})
}

func treeDefinition() definition {
	return defineTool[treeInput]("tree", "File Tree", readOnlyTool,
		"List files and directories inside the workspace, excluding blocked paths.", func(s *jsonschema.Schema) {
			integerRange(s.Properties["max_depth"], 1, 12)
			integerRange(s.Properties["max_entries"], 1, 3000)
		})
}

func searchDefinition() definition {
	return defineTool[searchInput]("search", "Search Files", readOnlyTool,
		"Find text (or a regex with regex=true) across the workspace and get each match with surrounding "+
			"lines. Use it before read to locate code, and instead of bash grep/rg. Results carry an edit_tag "+
			"when a complete current-file context is shown, so small edits can follow directly. kind=config "+
			"queries JSON/YAML/TOML paths such as jobs.*.steps[*].uses; scope limits the search to changed files "+
			"or added/removed diff lines. Paged: when has_more is true, pass next_cursor with the identical query "+
			"options. For structural code questions use ast_grep.", func(s *jsonschema.Schema) {
			stringRange(s.Properties["query"], 1, 4000)
			allowValues(s.Properties["kind"], "text", "config")
			integerRange(s.Properties["max_results"], 1, 2000)
			integerRange(s.Properties["context_before"], 0, 20)
			integerRange(s.Properties["context_after"], 0, 20)
			s.Properties["cursor"].MaxLength = new(4096)
			allowValues(s.Properties["scope"], "workspace", "changed_files", "diff_added", "diff_removed")
			s.Properties["base_ref"].MaxLength = new(256)
			allowValues(s.Properties["diff_target"], "worktree", "staged", "head")
			allowValues(s.Properties["config_format"], "auto", "json", "yaml", "toml")
			allowValues(s.Properties["intent"], "auto", "text", "symbol", "references", "impact")
		})
}

func astGrepDefinition() definition {
	return defineTool[astGrepInput]("ast_grep", "AST Grep", readOnlyTool,
		"Search source structurally with ast-grep/Tree-sitter syntax patterns or node kinds. This is "+
			"syntax-aware rather than type-aware semantic navigation. Complete current-file contexts return "+
			"edit_tag provenance and can be edited directly. Reuse next_cursor only with the exact same "+
			"structural query options.", func(s *jsonschema.Schema) {
			stringRange(s.Properties["pattern"], 1, 20000)
			stringRange(s.Properties["kind"], 1, 256)
			stringRange(s.Properties["language"], 1, 80)
			stringRange(s.Properties["selector"], 1, 256)
			allowValues(s.Properties["strictness"], "cst", "smart", "ast", "relaxed", "signature", "template")
			s.Properties["globs"].MaxItems = new(64)
			stringRange(s.Properties["globs"].Items, 1, 512)
			integerRange(s.Properties["max_results"], 1, 2000)
			integerRange(s.Properties["context_before"], 0, 20)
			integerRange(s.Properties["context_after"], 0, 20)
			s.Properties["cursor"].MaxLength = new(4096)
			integerRange(s.Properties["timeout_ms"], 1000, 60000)
		})
}
