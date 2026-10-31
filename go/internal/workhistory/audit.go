package workhistory

import (
	"context"

	"github.com/lyeith/codexpro/go/internal/audit"
	"github.com/lyeith/codexpro/go/internal/wire"
)

// AuditTarget resolves only an archived run's authenticated identity. A nil
// target means there is no string run reference: the engine must then use its
// current project catalogue for the source's project/server/unattributed rule.
// Archived projects are deliberately not treated as current configuration.
// This mirrors tools/context.ts resolveAuditTarget without a coordinator,
// workspace resolver, source inspection, database write, or claim operation.
func (s *Service) AuditTarget(_ context.Context, scope wire.Scope, args map[string]any, result wire.Result) *audit.Target {
	var reference any
	if wire.String(args, "action", "list") == "list" {
		// list ignores an input run_id, including an inaccessible one.
		reference = result.StructuredContent["run_id"]
	} else {
		reference = args["run_id"]
		if reference == nil {
			reference = result.StructuredContent["run_id"]
		}
	}
	id, selected := reference.(string)
	if !selected {
		return nil
	}
	unattributed := &audit.Target{Scope: "unattributed"}
	if s == nil || scope.PrincipalID == "" {
		return unattributed
	}
	run, err := s.require(s.principal(scope.PrincipalID), id, wire.String(args, "project_id", ""))
	if err != nil {
		// Unknown, ambiguous and inaccessible references must not expose the
		// requested run, project or workspace through the audit side channel.
		return unattributed
	}
	target := &audit.Target{Scope: "project", ProjectID: str(run["project_id"]), RunID: str(run["id"])}
	if workspace, ok := run["workspace"].(map[string]any); ok && workspace != nil {
		target.Scope = "workspace"
		target.WorkspaceID = str(workspace["id"])
	}
	return target
}
