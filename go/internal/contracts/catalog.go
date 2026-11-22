package contracts

import (
	"fmt"
	"slices"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/lyeith/codexpro/go/internal/wire"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const maxSafeInteger = 1<<53 - 1

type workspaceInput struct {
	WorkspaceID string `json:"workspace_id" jsonschema:"Required workspace_id returned by open_workspace for the selected project."`
}

type executionArgs struct {
	OperationKey string `json:"operation_key,omitempty"`
}

type toolInput struct {
	Execution executionArgs `json:"execution,omitempty" jsonschema:"Optional caller-provided operation metadata."`
	callerInput
}

type callerInput struct {
	Agent string `json:"agent,omitempty" jsonschema:"Optional caller label for activity tracking and, when configured, selection of explicitly addressed logical mail within the authenticated fleet account. Never used for authorization or verified conversation identity. Use 1-4 ASCII letters, digits or dashes; provide it on each call."`
}

type toolHints struct {
	readOnly, openWorld, destructive bool
	explicitIdempotency              bool
}

var (
	readOnlyTool         = toolHints{readOnly: true}
	workspaceOpenTool    = toolHints{readOnly: true, explicitIdempotency: true}
	createProjectTool    = toolHints{openWorld: true, explicitIdempotency: true}
	localDestructiveTool = toolHints{destructive: true, explicitIdempotency: true}
	openWorldMutation    = toolHints{openWorld: true, destructive: true, explicitIdempotency: true}
)

type definition struct {
	tool               mcp.Tool
	input              func() *jsonschema.Schema
	omitIdempotentHint bool
}

func defineTool[T any](name, title string, hints toolHints, description string, configure func(*jsonschema.Schema)) definition {
	meta := mcp.Meta{"securitySchemes": []map[string]string{{"type": "noauth"}}}
	if name == "import_file" {
		meta["openai/fileParams"] = []string{"file"}
	}
	return definition{
		tool: mcp.Tool{
			Name: name, Title: title, Description: description, Meta: meta,
			Annotations: &mcp.ToolAnnotations{
				ReadOnlyHint: hints.readOnly, OpenWorldHint: new(hints.openWorld), DestructiveHint: new(hints.destructive),
			},
		},
		omitIdempotentHint: !hints.explicitIdempotency,
		input: func() *jsonschema.Schema {
			schema := inferSchema[T]()
			schema.Schema = "http://json-schema.org/draft-07/schema#"
			agent := schema.Properties["agent"]
			agent.Pattern = wire.AgentPattern
			stringRange(agent, 1, wire.AgentMaxLength)
			if execution := schema.Properties["execution"]; execution != nil {
				stringRange(execution.Properties["operation_key"], 1, 160)
			}
			if configure != nil {
				configure(schema)
			}
			return schema
		},
	}
}

func inferSchema[T any]() *jsonschema.Schema {
	schema, err := jsonschema.For[T](nil)
	if err != nil {
		panic(fmt.Errorf("infer CodexPro input schema: %w", err))
	}
	useJSONInputTypes(schema)
	return schema
}

func useJSONInputTypes(schema *jsonschema.Schema) {
	// Inference permits nil slices and maps and closes all structs. Tool
	// arguments instead accept JSON arrays and objects, with optional fields
	// represented by omission. Definitions can opt into closed objects.
	if slices.Contains(schema.Types, "null") {
		for _, typ := range schema.Types {
			if typ != "null" {
				schema.Type = typ
			}
		}
		schema.Types = nil
	}
	if schema.Type == "object" && schema.Properties != nil {
		schema.AdditionalProperties = nil
	}
	for _, property := range schema.Properties {
		useJSONInputTypes(property)
	}
	if schema.Items != nil {
		useJSONInputTypes(schema.Items)
	}
}

func integerRange(schema *jsonschema.Schema, min, max float64) {
	schema.Minimum = &min
	schema.Maximum = &max
}

func lineNumber(schema *jsonschema.Schema) {
	integerRange(schema, 1, maxSafeInteger)
}

func stringRange(schema *jsonschema.Schema, min, max int) {
	schema.MinLength = &min
	schema.MaxLength = &max
}

func arrayRange(schema *jsonschema.Schema, min, max int) {
	schema.MinItems = &min
	schema.MaxItems = &max
}

func allowValues(schema *jsonschema.Schema, choices ...string) {
	schema.Enum = make([]any, len(choices))
	for i, choice := range choices {
		schema.Enum[i] = choice
	}
}

func closedObject() *jsonschema.Schema {
	return &jsonschema.Schema{Not: &jsonschema.Schema{}}
}

func catalog() []definition {
	return []definition{
		listProjectsDefinition(), createProjectDefinition(), serverConfigDefinition(),
		activityListDefinition(), activityGetDefinition(), activityStatusDefinition(), activityExportDefinition(),
		codexproSelfTestDefinition(), loadSkillDefinition(),
		listWorkspacesDefinition(), openCurrentWorkspaceDefinition(), openWorkspaceDefinition(),
		inspectWorkspaceDefinition(), treeDefinition(), searchDefinition(), astGrepDefinition(),
		readDefinition(), viewImageDefinition(), writeDefinition(), editDefinition(), applyPatchDefinition(), importFileDefinition(),
		bashDefinition(), startJobsDefinition(), queueJobsDefinition(), jobsDefinition(), stopJobsDefinition(),
		showChangesDefinition(), commitChangesDefinition(), batchDefinition(), workStatusDefinition(),
	}
}
