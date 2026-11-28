package audit

import (
	"context"
	"database/sql"

	"github.com/lyeith/codexpro/go/internal/wire"
)

// Retention keeps a bounded number of records per bucket, so under load a
// caller's recent actions can leave the retained journal within seconds. The
// agent_actions table keeps each agent label's newest actions, written in the
// same index transaction as the records and never trimmed by retention, so
// "what has this agent done since T" stays answerable for any T at or after
// agent_activity complete_since.

const agentActionsPerAgent = 32

type agentAction struct {
	Agent, ActionID, ToolName, Status, IdentitySource, OccurredAt string
}

func agentActionFrom(a Action) *agentAction {
	agent := text(a["agent"])
	if !wire.ValidAgent(agent) {
		return nil
	}
	return &agentAction{Agent: agent, ActionID: text(a["action_id"]), ToolName: text(a["tool_name"]), Status: text(a["status"]), IdentitySource: identitySource(a["identity"]), OccurredAt: text(a["occurred_at"])}
}

func identitySource(v any) string {
	switch id := v.(type) {
	case wire.Identity:
		return id.Source
	case *wire.Identity:
		if id != nil {
			return id.Source
		}
	case map[string]any:
		return text(id["source"])
	}
	return ""
}

func insertAgentAction(ctx context.Context, tx *sql.Tx, e entry, recordedAt string) error {
	a := e.Agent
	if a == nil {
		return nil
	}
	if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO agent_actions VALUES (?,?,?,?,?,?,?,?)", a.Agent, e.Sequence, a.ActionID, a.ToolName, a.Status, a.IdentitySource, a.OccurredAt, recordedAt); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, "DELETE FROM agent_actions WHERE agent=? AND sequence <= (SELECT sequence FROM agent_actions WHERE agent=? ORDER BY sequence DESC LIMIT 1 OFFSET ?)", a.Agent, a.Agent, agentActionsPerAgent)
	return err
}

// AgentActivity returns an agent's newest recorded actions, newest first, and
// the time from which that list is complete: the later of the journal's
// complete_since and the start of agent tracking. complete_since is null while
// appends fail or the journal has an unresolved gap.
func (j *Journal) AgentActivity(ctx context.Context, agent string) (map[string]any, error) {
	j.mu.RLock()
	defer j.mu.RUnlock()
	actions := []any{}
	out := map[string]any{"agent": agent, "actions": actions, "limit": agentActionsPerAgent, "tracked_since": nullString(j.state.AgentsSince), "complete_since": nil}
	if !j.options.Enabled || j.db == nil {
		return out, nil
	}
	if since, ok := j.coverageLocked()["complete_since"].(string); ok && j.state.AgentsSince != "" {
		if j.state.AgentsSince > since {
			since = j.state.AgentsSince
		}
		out["complete_since"] = since
	}
	rows, err := j.db.QueryContext(ctx, "SELECT sequence,action_id,tool_name,status,identity_source,occurred_at,recorded_at FROM agent_actions WHERE agent=? ORDER BY sequence DESC", agent)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var sequence int64
		var id, tool, status, source, occurred, recorded string
		if err = rows.Scan(&sequence, &id, &tool, &status, &source, &occurred, &recorded); err != nil {
			return nil, err
		}
		actions = append(actions, map[string]any{"sequence": sequence, "action_id": id, "tool_name": tool, "status": status, "identity_source": nullString(source), "occurred_at": occurred, "recorded_at": recorded})
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	out["actions"] = actions
	return out, nil
}
