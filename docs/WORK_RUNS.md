# Work status

Use `work_status` to inspect retained work-run records in a configured immutable archive. Call `work_status(action="list")` to discover the runs available to the authenticated principal. Use `project_id`, `state`, `claimed`, or `needs_attention` to filter the list.

Use a full run ID or a unique prefix with at least eight characters after `run_`. `action="get"` supports `section="packet"` for the saved briefing, `section="summary"` for status, or a specific section such as `todos`, `jobs`, `operations`, or `activity`. The tool also supports `history`, `read_document`, `search_memory`, and `operation` for retained records. The archive stores the last known state of each run. Use `jobs` for current Go job status and `show_changes` for current source changes.

Set `--work-history` or `CODEXPRO_GO_WORK_HISTORY` to an already consistent, checkpointed archive to enable its contents. Without an archive, the work status list is empty. Access follows the authenticated owner, with any explicit principal mapping stored beside the archive by the operator. Keep archived records private.
