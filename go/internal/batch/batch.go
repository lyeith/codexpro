// Package batch executes the compatibility batch contract. All operations are
// validated before any persistence or child call; stored definitions never carry
// credentials. The operation count bounds the only parallel fan-out to twelve.
package batch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"

	"github.com/lyeith/codexpro/go/internal/config"
	"github.com/lyeith/codexpro/go/internal/files"
	"github.com/lyeith/codexpro/go/internal/wire"
)

type Validator func(string, map[string]any) (map[string]any, error)
type JobLookup func(id, workspace string) (found, quiescent bool)

type Service struct {
	cfg       *config.Config
	resolver  wire.Resolver
	caller    wire.Caller
	validate  Validator
	mu        sync.RWMutex
	guard     *files.Service
	jobLookup JobLookup
}

func New(cfg *config.Config, resolver wire.Resolver, caller wire.Caller, validate func(string, map[string]any) (map[string]any, error)) (*Service, error) {
	guard, err := files.New(cfg, resolver)
	if err != nil {
		return nil, err
	}
	return &Service{cfg: cfg, resolver: resolver, caller: caller, validate: validate, guard: guard}, nil
}

func (s *Service) SetFiles(guard *files.Service) {
	if guard == nil {
		return
	}
	s.mu.Lock()
	s.guard = guard
	s.mu.Unlock()
}
func (s *Service) SetJobLookup(lookup func(id, workspace string) (found, quiescent bool)) {
	s.mu.Lock()
	s.jobLookup = lookup
	s.mu.Unlock()
}
func (s *Service) fileService() *files.Service { s.mu.RLock(); defer s.mu.RUnlock(); return s.guard }

func batchError(code, message string) error {
	return &wire.Error{Code: code, Message: message, Data: map[string]any{"retry_unchanged": false}}
}
func invalid(message string) error { return batchError("batch_args_invalid", message) }
func clone(args map[string]any) map[string]any {
	out := make(map[string]any, len(args))
	for key, value := range args {
		out[key] = value
	}
	return out
}
func owns(args map[string]any, key string) bool { _, ok := args[key]; return ok }

var allowed = map[string]bool{"tree": true, "search": true, "ast_grep": true, "read": true, "inspect_workspace": true, "show_changes": true, "write": true, "edit": true, "apply_patch": true, "bash": true}
var parallelSafe = map[string]bool{"tree": true, "search": true, "ast_grep": true, "read": true, "inspect_workspace": true, "show_changes": true}
var operationID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,79}$`)

func fileMutation(name string) bool {
	return name == "write" || name == "edit" || name == "apply_patch"
}
func controlled(name string) bool { return fileMutation(name) || name == "bash" }

type operation struct {
	Index           int
	ID, Tool        string
	Args, Validated map[string]any
}
type plan struct {
	Workspace                                                             wire.Workspace
	Operations                                                            []operation
	Source, Path, Tag, Mode, Efficiency                                   string
	ContinueOnError, PersistenceDefault, PersistenceRequested, CanPersist bool
	AutoStored, Persisted, GitExcluded                                    bool
	Pruned                                                                []string
	Start                                                                 int
}

func array(value any) ([]any, bool) {
	switch v := value.(type) {
	case []any:
		return v, true
	case []map[string]any:
		out := make([]any, len(v))
		for i := range v {
			out[i] = v[i]
		}
		return out, true
	}
	return nil, false
}

func unknownKeys(value map[string]any, valid ...string) []string {
	known := map[string]bool{}
	for _, key := range valid {
		known[key] = true
	}
	extra := []string{}
	for key := range value {
		if !known[key] {
			extra = append(extra, key)
		}
	}
	sort.Strings(extra)
	return extra
}

func unrecognized(keys []string) string {
	quoted := make([]string, len(keys))
	for i, key := range keys {
		quoted[i] = fmt.Sprintf("%q", key)
	}
	label := "Unrecognized key: "
	if len(keys) > 1 {
		label = "Unrecognized keys: "
	}
	return label + strings.Join(quoted, ", ")
}

// Stored files pass the same strict structural gate as inline operations. Child
// input schemas are validated separately by the transport's registered catalog.
func parseOperations(value any) ([]any, []string) {
	list, ok := array(value)
	if !ok {
		return nil, []string{"operations: Invalid input: expected array, received " + typeName(value)}
	}
	issues := []string{}
	if len(list) < 1 {
		issues = append(issues, "operations: Too small: expected array to have >=1 items")
	}
	if len(list) > 12 {
		issues = append(issues, "operations: Too big: expected array to have <=12 items")
	}
	for i, item := range list {
		prefix := fmt.Sprintf("operations.%d", i)
		op, ok := item.(map[string]any)
		if !ok {
			issues = append(issues, prefix+": Invalid input: expected object, received "+typeName(item))
			continue
		}
		if id, exists := op["id"]; exists {
			if text, ok := id.(string); !ok {
				issues = append(issues, prefix+".id: Invalid input: expected string, received "+typeName(id))
			} else if !operationID.MatchString(text) {
				issues = append(issues, prefix+".id: Invalid string: must match pattern /^[A-Za-z0-9][A-Za-z0-9._-]{0,79}$/")
			}
		}
		if tool, ok := op["tool"].(string); !ok || !allowed[tool] {
			issues = append(issues, prefix+`.tool: Invalid option: expected one of "tree"|"search"|"ast_grep"|"read"|"inspect_workspace"|"show_changes"|"write"|"edit"|"apply_patch"|"bash"`)
		}
		if args, exists := op["args"]; exists {
			if _, ok := args.(map[string]any); !ok {
				issues = append(issues, prefix+".args: Invalid input: expected record, received "+typeName(args))
			}
		}
		if extra := unknownKeys(op, "id", "tool", "args"); len(extra) > 0 {
			issues = append(issues, prefix+": "+unrecognized(extra))
		}
	}
	return list, issues
}

func typeName(value any) string {
	switch value.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case bool:
		return "boolean"
	case float64, float32, int, int64, json.Number:
		return "number"
	case []any, []map[string]any:
		return "array"
	case map[string]any:
		return "object"
	}
	return "undefined"
}

func number(value any) (float64, bool) {
	switch v := value.(type) {
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case float64:
		return v, true
	case json.Number:
		n, err := v.Float64()
		return n, err == nil
	}
	return 0, false
}

func parseStored(path string, value any) ([]any, string, bool, error) {
	definition, ok := value.(map[string]any)
	if !ok {
		return nil, "", false, definitionError(path, "definition: Invalid input: expected object, received "+typeName(value))
	}
	issues := []string{}
	if version, ok := number(definition["version"]); !ok || version != 1 {
		issues = append(issues, "version: Invalid input: expected 1")
	}
	mode, _ := definition["mode"].(string)
	if mode != "serial" && mode != "parallel" {
		issues = append(issues, `mode: Invalid option: expected one of "serial"|"parallel"`)
	}
	keepGoing, ok := definition["continue_on_error"].(bool)
	if !ok {
		issues = append(issues, "continue_on_error: Invalid input: expected boolean, received "+typeName(definition["continue_on_error"]))
	}
	operations, operationIssues := parseOperations(definition["operations"])
	issues = append(issues, operationIssues...)
	if extra := unknownKeys(definition, "version", "mode", "continue_on_error", "operations"); len(extra) > 0 {
		issues = append(issues, "definition: "+unrecognized(extra))
	}
	if len(issues) > 0 {
		return nil, "", false, definitionError(path, strings.Join(issues, "; "))
	}
	return operations, mode, keepGoing, nil
}

func canonicalPath(path string) (string, error) {
	probe, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	segments := []string{}
	for {
		real, err := filepath.EvalSymlinks(probe)
		if err == nil {
			all := append([]string{real}, segments...)
			return filepath.Join(all...), nil
		}
		if !os.IsNotExist(err) && !isNotDirectory(err) {
			return "", err
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return filepath.Abs(path)
		}
		segments = append([]string{filepath.Base(probe)}, segments...)
		probe = parent
	}
}
func isNotDirectory(err error) bool { return errors.Is(err, syscall.ENOTDIR) }

func (s *Service) preflight(ctx context.Context, scope wire.Scope, args map[string]any) (plan, error) {
	w, err := s.resolver.Resolve(ctx, scope, wire.String(args, "workspace_id", ""))
	if err != nil {
		return plan{}, err
	}
	inline, inlineSupplied := array(args["operations"])
	path := wire.String(args, "path", "")
	pathSupplied := strings.TrimSpace(path) != ""
	if inlineSupplied == pathSupplied {
		return plan{}, invalid("Provide exactly one of operations or path.")
	}
	if owns(args, "from") && owns(args, "from_index") {
		return plan{}, invalid("Provide from or from_index, not both.")
	}
	if inlineSupplied && (owns(args, "from") || owns(args, "from_index")) {
		return plan{}, invalid("from and from_index apply only when executing a stored batch path.")
	}
	if pathSupplied && owns(args, "persist") {
		return plan{}, invalid("persist applies only to inline operations. A stored path is already persistent.")
	}
	p := plan{Workspace: w, Source: "inline", Mode: wire.String(args, "mode", "serial"), ContinueOnError: wire.Bool(args, "continue_on_error", false), Pruned: []string{}, CanPersist: !s.cfg.Bool("connectionTest", false) && s.cfg.String("writeMode", "workspace") == "workspace"}
	if pathSupplied {
		if owns(args, "mode") || owns(args, "continue_on_error") {
			return plan{}, invalid("Stored batch mode and continue_on_error come from the JSON file. Edit the file instead of overriding them.")
		}
		loaded, err := s.load(w, path)
		if err != nil {
			return plan{}, err
		}
		inline, p.Mode, p.ContinueOnError, err = parseStored(loaded.Path, loaded.Raw)
		if err != nil {
			return plan{}, err
		}
		p.Source = "file"
		p.Path = loaded.Path
		p.AutoStored = loaded.AutoStored
		p.Persisted = true
		if p.AutoStored {
			p.Tag = strings.ToUpper(strings.TrimSuffix(filepath.Base(p.Path), filepath.Ext(p.Path)))
		}
	} else {
		var issues []string
		inline, issues = parseOperations(inline)
		if len(issues) > 0 {
			return plan{}, invalid(strings.Join(issues, "; "))
		}
		if p.Mode != "serial" && p.Mode != "parallel" {
			return plan{}, invalid("mode must be serial or parallel.")
		}
	}
	seen := map[string]bool{}
	mutations, verification := []int{}, []int{}
	for i, raw := range inline {
		item := raw.(map[string]any)
		op := operation{Index: i, ID: wire.String(item, "id", fmt.Sprintf("op_%d", i+1)), Tool: wire.String(item, "tool", ""), Args: clone(wire.Object(item, "args"))}
		if seen[op.ID] {
			return plan{}, batchError("batch_duplicate_id", "Duplicate batch operation id: "+op.ID)
		}
		seen[op.ID] = true
		if !allowed[op.Tool] {
			return plan{}, batchError("batch_child_not_allowed", fmt.Sprintf("Tool %s is not allowed inside batch.", op.Tool))
		}
		if owns(op.Args, "workspace_id") {
			return plan{}, invalid(fmt.Sprintf("Operation %s must not provide workspace_id; use the outer batch workspace_id.", op.ID))
		}
		for _, key := range []string{"execution", "attempt_token", "session_token", "session_id"} {
			if owns(op.Args, key) {
				return plan{}, invalid(fmt.Sprintf("Operation %s must not contain credentials. Use the outer execution envelope or outer session_id for Bash session authorization.", op.ID))
			}
		}
		if s.caller == nil {
			return plan{}, batchError("batch_child_not_allowed", fmt.Sprintf("Tool %s is not available in the current CodexPro mode.", op.Tool))
		}
		if s.validate == nil {
			return plan{}, batchError("batch_child_not_allowed", fmt.Sprintf("Tool %s has no registered batch validator.", op.Tool))
		}
		child := clone(op.Args)
		child["workspace_id"] = w.ID
		if op.Tool == "bash" {
			if session, exists := args["session_id"]; exists {
				child["session_id"] = session
			}
		}
		op.Validated, err = s.validate(op.Tool, child)
		if err != nil {
			return plan{}, err
		}
		if op.Validated == nil {
			return plan{}, batchError("batch_child_not_allowed", fmt.Sprintf("Tool %s has no registered batch validator.", op.Tool))
		}
		p.Operations = append(p.Operations, op)
		if fileMutation(op.Tool) {
			mutations = append(mutations, i)
		}
		if op.Tool == "bash" {
			verification = append(verification, i)
		}
	}
	p.PersistenceDefault = len(verification) > 0
	p.PersistenceRequested = pathSupplied || wire.Bool(args, "persist", p.PersistenceDefault)
	if inlineSupplied && wire.Bool(args, "persist", false) && !p.CanPersist {
		return plan{}, batchError("batch_persist_disabled", "persist=true requires workspace write mode and is unavailable in connection-test mode.")
	}
	if inlineSupplied && len(p.Operations) <= 2 && len(verification) == 0 {
		if len(mutations) == 1 {
			p.Efficiency = "Prefer the direct mutation tool for a one-off one-file change; its result already includes the diff. Add batch for coordinated distinct-file mutations, actual Bash verification, or a resumable workflow."
		} else if len(mutations) == 0 {
			p.Efficiency = "Prefer direct tool calls for one or two ordinary reads. Use one consolidated parallel batch for three or more independent reads instead of several tiny batches."
		}
	}
	for _, i := range mutations {
		if p.Operations[i].Tool == "apply_patch" && len(mutations) > 1 {
			return plan{}, batchError("batch_mutation_conflict", fmt.Sprintf("apply_patch batch operation %s must be the only file-mutation child because one patch may already span several files. Use separate write/edit children only for distinct single-file targets.", p.Operations[i].ID))
		}
	}
	targets := map[string]string{}
	for _, i := range mutations {
		op := p.Operations[i]
		if op.Tool == "apply_patch" {
			continue
		}
		abs, rel, err := s.fileService().ResolvePath(w, wire.String(op.Validated, "path", ""), true)
		if err != nil {
			return plan{}, err
		}
		canonical, err := canonicalPath(abs)
		if err != nil {
			return plan{}, err
		}
		if previous, ok := targets[canonical]; ok {
			return plan{}, batchError("batch_mutation_conflict", fmt.Sprintf("Batch operations %s and %s both mutate %s. Combine all changes to one file into one tagged edit or one write operation.", previous, op.ID, rel))
		}
		targets[canonical] = op.ID
	}
	if len(mutations)+len(verification) > 0 {
		if p.Mode != "serial" {
			return plan{}, batchError("batch_mode_serial_required", "A batch containing a file mutation or Bash verification must use mode=serial.")
		}
		if p.ContinueOnError {
			return plan{}, invalid("continue_on_error is allowed only for batches containing read-only child tools.")
		}
	}
	if checkpoint, exists := args["checkpoint"]; exists && checkpoint != nil && checkpoint != false {
		return plan{}, invalid("checkpoint requires a managed workspace operation key and mode=serial.")
	}
	for _, i := range verification {
		op := &p.Operations[i]
		command := wire.String(op.Validated, "command", "")
		if strings.TrimSpace(command) == "" {
			return plan{}, batchError("args_invalid", "command is required.")
		}
		if err = s.assertSession(wire.String(op.Validated, "session_id", "")); err != nil {
			return plan{}, err
		}
		if wire.Bool(op.Validated, "background", false) {
			return plan{}, batchError("args_invalid", fmt.Sprintf("Batch operation %s: background bash is not allowed inside a batch; start it with the bash tool directly.", op.ID))
		}
		op.Validated["on_timeout"] = "kill"
	}
	if len(mutations) > 0 {
		last := mutations[len(mutations)-1]
		for _, i := range verification {
			if i < last {
				return plan{}, batchError("batch_verification_order", fmt.Sprintf("Verification Bash operation %s appears before the final file mutation. Put all verification commands after every write/edit/apply_patch operation.", p.Operations[i].ID))
			}
		}
	}
	if p.Mode == "parallel" {
		unsafe := []string{}
		for _, op := range p.Operations {
			if !parallelSafe[op.Tool] {
				unsafe = append(unsafe, op.Tool)
			}
		}
		if len(unsafe) > 0 {
			return plan{}, batchError("batch_parallel_unsafe_child", "Parallel batch contains non-parallel-safe tools: "+strings.Join(unsafe, ", ")+". Use mode=serial.")
		}
	}
	if from, ok := args["from"].(string); ok {
		p.Start = -1
		ids := []string{}
		for i, op := range p.Operations {
			ids = append(ids, op.ID)
			if op.ID == strings.TrimSpace(from) {
				p.Start = i
			}
		}
		if p.Start < 0 {
			return plan{}, batchError("batch_resume_invalid", fmt.Sprintf("Batch operation id not found: %s. Available ids: %s.", strings.TrimSpace(from), strings.Join(ids, ", ")))
		}
	} else if index, exists := args["from_index"]; exists {
		n, ok := number(index)
		if !ok || n < 0 || n != math.Trunc(n) || n >= float64(len(p.Operations)) {
			return plan{}, batchError("batch_resume_invalid", fmt.Sprintf("from_index %v is outside this %d-operation batch.", index, len(p.Operations)))
		}
		p.Start = int(n)
	}
	return p, nil
}

func (s *Service) assertSession(requested string) error {
	configured := s.cfg.String("bashSessionId", "")
	required := s.cfg.Bool("requireBashSession", false)
	requested = strings.TrimSpace(requested)
	if configured == "" {
		if required {
			return batchError("bash_session_required", "bash session guard is enabled but no server bash session id is configured.")
		}
		return nil
	}
	recovery := map[string]any{"tool": "bash", "message": "Retry with the server bash session id.", "args": map[string]any{"session_id": configured}}
	if requested == "" {
		if required {
			return &wire.Error{Code: "bash_session_required", Message: fmt.Sprintf("bash session id is required. Retry with session_id=%q.", configured), Data: map[string]any{"retry_unchanged": false, "recovery": recovery}}
		}
		return nil
	}
	if requested != configured {
		return &wire.Error{Code: "bash_session_mismatch", Message: fmt.Sprintf("bash session id mismatch. This CodexPro server accepts session_id=%q.", configured), Data: map[string]any{"retry_unchanged": false, "recovery": recovery}}
	}
	return nil
}

func (s *Service) Call(ctx context.Context, scope wire.Scope, name string, args map[string]any) (wire.Result, error) {
	if name != "batch" {
		return wire.Result{}, wire.Errorf("tool_not_found", "Unknown batch tool: %s", name)
	}
	p, err := s.preflight(ctx, scope, args)
	if err != nil {
		return wire.ErrorResult(err), nil
	} // No child or store effects: durable failure receipt.
	if p.Source == "file" {
		stored, err := s.maintain(ctx, p.Workspace, p.Path)
		if err != nil {
			return wire.Result{}, err
		}
		p.GitExcluded = stored.GitExcluded
		p.Pruned = stored.Pruned
	} else if p.PersistenceRequested && p.CanPersist {
		definition := Definition{Version: DefinitionVersion, Mode: p.Mode, ContinueOnError: p.ContinueOnError, Operations: make([]StoredOperation, len(p.Operations))}
		for i, op := range p.Operations {
			saved := map[string]any{}
			for key := range op.Args {
				if value, ok := op.Validated[key]; ok {
					saved[key] = value
				}
			}
			definition.Operations[i] = StoredOperation{ID: op.ID, Tool: op.Tool, Args: saved}
		}
		stored, err := s.materialize(ctx, scope, p.Workspace, definition)
		if err != nil {
			return wire.Result{}, err
		}
		p.Path = stored.Path
		p.Tag = stored.Tag
		p.AutoStored = true
		p.Persisted = true
		p.GitExcluded = stored.GitExcluded
		p.Pruned = stored.Pruned
	}
	return s.execute(ctx, scope, args, p), nil
}
