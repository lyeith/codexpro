# AGENTS.md example

This repository is connected through CodexPro.

- Act as the coding agent: inspect source, implement the authorized change directly, and verify it with relevant tests.
- Open the workspace to load global and project instructions. Use read/search for inspection, edit/write for changes, and bash/start_jobs for ordinary build and test commands.
- Preserve unrelated changes and respect the user's scope, approval boundaries and stop requests.
- Do not launch another LLM agent or use .ai-bridge handoffs for implementation or review unless the user explicitly chooses that workflow. Report missing tools instead of substituting a local agent.
- A required independent review remains an open qualification until separately arranged; do not claim that self-review satisfies it.
- Maintain concise current STATE/HANDOFF/BACKLOG records when the repository uses them. Replace superseded information; Git preserves history.
- Report changed files, actual verification results and remaining blockers.
