# Error codes

Every CodexPro tool error carries a stable `error_code` in `structuredContent`,
plus `retry_unchanged` (false means: do not resend the same request) and, where
useful, a `recovery` hint naming the tool to call next. Some errors add fields
(for example `known_project_ids`).

| Code | Meaning | What to do |
|---|---|---|
| `args_invalid` | Arguments failed schema validation. | Fix the named field. A missing `workspace_id` comes from `list_projects` or `open_workspace`. |
| `project_unknown` | `project_id` is not in the catalog. | Use one of `known_project_ids`; if the project is missing, ask the user to add it. |
| `project_exists` | `create_project` id or root already exists. | Pick another id/root. |
| `project_root_not_allowed` | New project root is outside allowed/creation roots. | Choose a parent from `list_projects`. |
| `workspace_unknown` | `workspace_id` is not open and not a configured project. | Use `known_workspace_ids` or `list_projects`. |
| `workspace_root_invalid` | Root path missing or not a directory. | Use a catalog project. |
| `workspace_root_not_allowed` | Root is outside the allowed roots. | Use a catalog project. |
| `path_blocked` | Path matches a blocked glob (`blocked_reason`: `secret` or `artifact`). | Secrets are never readable; artifacts can be verified with `bash`. |
| `path_outside_workspace` | Path (or a symlink) escapes the workspace. | Use a path inside the workspace. |
| `path_symlink_refused` | Refusing to write through a symlink. | Write to the real file. |
| `path_not_file` / `path_not_directory` | Wrong node type. | Check with `tree`. |
| `file_too_large` | File or content exceeds the configured limit. | Read a line range or search instead. |
| `file_binary` | Binary file. | Use `view_image` for images. |
| `file_exists` | `overwrite=false` and the file exists. | Use `edit`, or allow overwrite. |
| `file_changed` | The file changed between read and write. | Re-read, then resend with the new `expected_sha256`. |
| `secret_content_blocked` | Content looks like a credential. | Use placeholders such as `[REDACTED_SECRET]`. |
| `edit_tag_invalid` / `edit_tag_unknown` / `edit_tag_stale` / `edit_range_unseen` | Tagged-edit provenance failed. | Follow `recovery` (usually re-read the file). |
| `patch_format_invalid` / `patch_context_stale` / `patch_apply_failed` | `apply_patch` failed. | Follow `recovery`; prefer `edit` for one-file changes. |
| `bash_disabled` | Bash is off on this server. | Do not retry. |
| `bash_blocked` | Command not allowed in safe mode (`bash_context`: `safe-mode` or `batch-verification`). | Use an allowlisted verification command. |
| `bash_session_required` / `bash_session_mismatch` | Session guard. | Retry with the `session_id` from `recovery.args`. |
| `job_not_found` | `job_id` is unknown for this workspace. | Use `known_job_ids` or list with `jobs`. |
| `job_limit_reached` | Too many background jobs (6 per workspace, 12 per server by default); `capacity` says how many more may start. | Collect with `jobs(job_ids, wait_ms)` or `stop_jobs`, or start fewer. |
| `job_start_failed` | The command process could not be spawned. | Check the message. |
| `batch_args_invalid` / `batch_file_invalid` / `batch_duplicate_id` / `batch_child_not_allowed` / `batch_persist_disabled` / `batch_mutation_conflict` / `batch_mode_serial_required` / `batch_verification_order` / `batch_parallel_unsafe_child` / `batch_resume_invalid` | A batch was rejected before any operation ran. | Fix the batch as the message says; never resend unchanged. |
| `nothing_to_commit` | `commit_changes` found no stageable change. | Review with `show_changes`. |
| `git_unavailable` / `git_command_failed` | Git is missing, not a repository, or the command failed. | Read the message. |
| `search_cursor_invalid` | `next_cursor` does not match the query. | Restart the search without a cursor. |
