/** Short references are resolved only against runs the caller may access. */
export class AmbiguousRunReferenceError extends Error {
  constructor() { super("Ambiguous run_id. Use a longer prefix or the full ID."); }
}

export function resolveRunReference<T extends { id: string }>(runs: readonly T[], reference: string): T | undefined {
  // Exact IDs, including older formats, always retain their original meaning.
  const exact = runs.find(run => run.id === reference);
  if (exact) return exact;
  const compact = /^(run_[A-Za-z0-9_-]{8,})(?:…|\.\.\.)([A-Za-z0-9_-]{4,})$/.exec(reference);
  if (!compact && !/^run_[A-Za-z0-9_-]{8,}$/.test(reference)) return undefined;
  const prefix = compact?.[1] ?? reference;
  const suffix = compact?.[2];
  let match: T | undefined;
  for (const run of runs) {
    if (!run.id.startsWith(prefix) || (suffix && (!run.id.endsWith(suffix) || run.id.length <= prefix.length + suffix.length))) continue;
    if (match) throw new AmbiguousRunReferenceError();
    match = run;
  }
  return match;
}
