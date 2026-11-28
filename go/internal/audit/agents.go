package audit

import (
	"context"
	"database/sql"
	"errors"
	"sort"

	"github.com/lyeith/codexpro/go/internal/wire"
)

// Retention keeps a bounded number of records per bucket, so under load a
// caller's recent actions can leave the retained journal within seconds. The
// agent_actions table keeps each agent label's newest actions, written in the
// same index transaction as the records and never trimmed by retention, so
// "what has this agent done since T" stays answerable for any T at or after
// agent_activity complete_since.
//
// Each agent owns agentActionsPerAgent fixed slots in a clustered
// (agent, slot) table. Its n-th action overwrites slot n mod
// agentActionsPerAgent in place, so an append rewrites one row of a fixed set
// and never inserts, deletes, splits pages or touches the freelist once the
// agent's slots exist. A read is one key-prefix scan of at most
// agentActionsPerAgent rows.

const agentActionsPerAgent = 32

const agentActionsSchema = "CREATE TABLE agent_actions(agent TEXT NOT NULL,slot INTEGER NOT NULL,n INTEGER NOT NULL,sequence INTEGER NOT NULL,action_id TEXT NOT NULL,tool_name TEXT NOT NULL,status TEXT NOT NULL,identity_source TEXT NOT NULL,occurred_at TEXT NOT NULL,recorded_at TEXT NOT NULL,PRIMARY KEY(agent,slot)) WITHOUT ROWID"

// migrateAgentActions creates the slot table, replacing the unbounded-row
// table of release cbd70be. It reports a reset so agent tracking restarts from
// now: rows written before the replacement are not carried over.
func (j *Journal) migrateAgentActions() (bool, error) {
	var current string
	err := j.db.QueryRow("SELECT sql FROM sqlite_master WHERE type='table' AND name='agent_actions'").Scan(&current)
	switch {
	case err == nil && current == agentActionsSchema:
		return false, nil
	case err == nil:
		if _, err = j.db.Exec("DROP TABLE agent_actions"); err != nil {
			return false, err
		}
	case !errors.Is(err, sql.ErrNoRows):
		return false, err
	}
	_, err = j.db.Exec(agentActionsSchema)
	return true, err
}

// loadAgentCounters restores each agent's next action number from the
// durable slots. load runs it before recovery, so a reopen after a failed
// batch drops counters that advanced in the rolled-back transaction.
func (j *Journal) loadAgentCounters() error {
	j.agentNext = map[string]int64{}
	rows, err := j.db.Query("SELECT agent,max(n) FROM agent_actions GROUP BY agent")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var agent string
		var n int64
		if err = rows.Scan(&agent, &n); err != nil {
			return err
		}
		j.agentNext[agent] = n + 1
	}
	return rows.Err()
}

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

func (j *Journal) recordAgentAction(ctx context.Context, tx *sql.Tx, e entry, recordedAt string) error {
	a := e.Agent
	if a == nil {
		return nil
	}
	n := j.agentNext[a.Agent]
	_, err := tx.ExecContext(ctx, "INSERT INTO agent_actions VALUES (?,?,?,?,?,?,?,?,?,?) ON CONFLICT(agent,slot) DO UPDATE SET n=excluded.n,sequence=excluded.sequence,action_id=excluded.action_id,tool_name=excluded.tool_name,status=excluded.status,identity_source=excluded.identity_source,occurred_at=excluded.occurred_at,recorded_at=excluded.recorded_at",
		a.Agent, n%agentActionsPerAgent, n, e.Sequence, a.ActionID, a.ToolName, a.Status, a.IdentitySource, a.OccurredAt, recordedAt)
	if err == nil {
		j.agentNext[a.Agent] = n + 1
	}
	return err
}

// AgentActivity returns an agent's newest recorded actions, newest first, and
// the time from which that list is complete: the later of the journal's
// complete_since and the start of agent tracking. complete_since is null while
// appends fail or the journal has an unresolved gap.
func (j *Journal) AgentActivity(ctx context.Context, agent string) (map[string]any, error) {
	j.mu.RLock()
	defer j.mu.RUnlock()
	out := map[string]any{"agent": agent, "actions": []any{}, "limit": agentActionsPerAgent, "tracked_since": nullString(j.state.AgentsSince), "complete_since": nil}
	if !j.options.Enabled || j.db == nil {
		return out, nil
	}
	if since, ok := j.coverageLocked()["complete_since"].(string); ok && j.state.AgentsSince != "" {
		if j.state.AgentsSince > since {
			since = j.state.AgentsSince
		}
		out["complete_since"] = since
	}
	rows, err := j.db.QueryContext(ctx, "SELECT n,sequence,action_id,tool_name,status,identity_source,occurred_at,recorded_at FROM agent_actions WHERE agent=?", agent)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type row struct {
		n    int64
		view map[string]any
	}
	found := []row{}
	for rows.Next() {
		var n, sequence int64
		var id, tool, status, source, occurred, recorded string
		if err = rows.Scan(&n, &sequence, &id, &tool, &status, &source, &occurred, &recorded); err != nil {
			return nil, err
		}
		found = append(found, row{n, map[string]any{"sequence": sequence, "action_id": id, "tool_name": tool, "status": status, "identity_source": nullString(source), "occurred_at": occurred, "recorded_at": recorded}})
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(found, func(a, b int) bool { return found[a].n > found[b].n })
	actions := make([]any, 0, len(found))
	for _, r := range found {
		actions = append(actions, r.view)
	}
	out["actions"] = actions
	return out, nil
}
