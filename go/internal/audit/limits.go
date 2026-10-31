package audit

import "context"

type operationalLimits struct {
	maxBytes                      int64
	retainActions, maxOutputBytes int
}

// SetLimits atomically publishes one runtime configuration snapshot. New
// durable writer batches apply retention using this snapshot; status and export
// observe it immediately. Enabled, storage paths and segment size remain fixed.
func (j *Journal) SetLimits(maxBytes int64, retainActions, maxOutputBytes int) {
	if maxBytes <= 0 {
		maxBytes = 8 * 1024 * 1024
	}
	if retainActions <= 0 {
		retainActions = 350
	}
	if maxOutputBytes <= 0 {
		maxOutputBytes = 120000
	}
	j.limits.Store(&operationalLimits{maxBytes, retainActions, maxOutputBytes})
}
func (j *Journal) currentLimits() operationalLimits {
	if value := j.limits.Load(); value != nil {
		return *value
	}
	return operationalLimits{j.options.MaxBytes, j.options.RetainActions, j.options.MaxOutputBytes}
}
func (j *Journal) optionsWithLimits() Options {
	options := j.options
	value := j.currentLimits()
	options.MaxBytes, options.RetainActions, options.MaxOutputBytes = value.maxBytes, value.retainActions, value.maxOutputBytes
	return options
}

type projectMutation struct {
	actionID string
	change   map[string]any
}

// cacheProjectAction and pruneProjectChanges run under the writer lock, or
// before startup exposes the journal. They retain only the compact evidence
// needed by the legacy 500-mutation last-change projection.
func (j *Journal) cacheProjectAction(action Action) {
	project := text(action["project_id"])
	if project == "" || !boolValue(action["mutating"]) {
		return
	}
	if j.projectMutations == nil {
		j.projectMutations = map[string][]projectMutation{}
	}
	observation := projectMutation{actionID: text(action["action_id"])}
	before, after := object(action["git_before"]), object(action["git_after"])
	if number(action["changed_path_count"]) > 0 || (text(before["head"]) != "" && text(after["head"]) != "" && before["head"] != after["head"]) {
		paths := array(action["changed_paths"])
		if paths == nil {
			paths = []any{}
		}
		if len(paths) > 10 {
			paths = paths[:10]
		}
		observation.change = map[string]any{"at": action["finished_at"], "action_id": action["action_id"], "changed_paths": append([]any{}, paths...)}
		if workspace, ok := action["workspace_id"]; ok {
			observation.change["workspace_id"] = workspace
		}
	}
	records := append(j.projectMutations[project], observation)
	if len(records) > 500 {
		records = append([]projectMutation(nil), records[len(records)-500:]...)
	}
	j.projectMutations[project] = records
}
func (j *Journal) pruneProjectChanges() {
	j.lastProjectChanges = make(map[string]map[string]any, len(j.projectMutations))
	for project, records := range j.projectMutations {
		kept := records[:0]
		for _, record := range records {
			if _, ok := j.byID[record.actionID]; ok {
				kept = append(kept, record)
				if record.change != nil {
					j.lastProjectChanges[project] = record.change
				}
			}
		}
		if len(kept) == 0 {
			delete(j.projectMutations, project)
		} else {
			j.projectMutations[project] = kept
		}
	}
}

// LastRecordedProjectChange performs no filesystem or SQL reads. Its result is
// a detached copy and cannot mutate the cached committed observation.
func (j *Journal) LastRecordedProjectChange(ctx context.Context, projectID string) (map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	j.mu.RLock()
	defer j.mu.RUnlock()
	value := j.lastProjectChanges[projectID]
	if value == nil {
		return nil, nil
	}
	return map[string]any(clone(Action(value))), nil
}
