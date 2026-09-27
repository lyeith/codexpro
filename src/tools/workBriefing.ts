import { truncateUtf8WithMarker } from "./shared.js";

const bytes = (value: unknown) => Buffer.byteLength(JSON.stringify(value));

/** Protect the assignment from bulk history. Full text remains in versioned
 * spec/handoff documents; task and acceptance pages use the existing sections. */
export function workBriefing(packet: any, maxBytes: number, offset = 0, limit = 10) {
  const documents = packet.documents.filter((d: any) => ["spec", "handoff"].includes(d.kind))
    .map((d: any) => ({ id: d.id, revision: d.revision, kind: d.kind }));
  const brief: any = {
    objective: packet.objective, scope: packet.scope,
    checkpoint: packet.checkpoint ? { summary: packet.checkpoint.summary, next_action: packet.checkpoint.next_action } : null,
    documents, todos: [], acceptance: [], pages: {}, truncated_fields: []
  };
  for (const section of ["todos", "acceptance"]) {
    brief.pages[section] = { offset, total_items: packet[section].length, next_offset: offset < packet[section].length ? offset : null };
  }
  // Never drop objective/scope/handoff wholesale. When text itself is too large,
  // keep an explicit excerpt and exact document references for lossless reads.
  const fields = ["objective", "scope", "checkpoint.summary", "checkpoint.next_action"];
  while (bytes(brief) > maxBytes) {
    const candidates = fields.map(field => {
      const parts = field.split("."); const owner = parts.length === 2 ? brief[parts[0]] : brief;
      return { field, owner, key: parts.at(-1)!, size: owner ? bytes(owner[parts.at(-1)!]) : 0 };
    }).filter(item => item.size > 36).sort((a, b) => b.size - a.size);
    if (!candidates.length) break;
    const item = candidates[0];
    item.owner[item.key] = truncateUtf8WithMarker(item.owner[item.key], Math.floor(Buffer.byteLength(item.owner[item.key]) / 2), "…").value;
    if (!brief.truncated_fields.includes(item.field)) brief.truncated_fields.push(item.field);
  }
  // Alternate sections so a large task list cannot consume all acceptance space.
  for (let index = offset; index < offset + limit; index++) {
    let added = false;
    for (const section of ["todos", "acceptance"]) {
      const items = packet[section], page = brief.pages[section];
      if (index >= items.length || page.next_offset !== index) continue;
      brief[section].push(items[index]);
      page.next_offset = index + 1 < items.length ? index + 1 : null;
      if (bytes(brief) > maxBytes) { brief[section].pop(); page.next_offset = index; }
      else added = true;
    }
    if (!added) break;
  }
  return brief;
}
