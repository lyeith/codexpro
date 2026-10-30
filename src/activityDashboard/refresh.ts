/**
 * Reconcile server-rendered dashboard fragments without replacing retained nodes.
 * Kept self-contained so the same implementation can be embedded in the page
 * and exercised against a DOM in unit tests.
 */
export function reconcileDashboardElement(current: Element, incoming: Element, preserveRoot = true): void {
  const key = (node: Node): string | null => {
    if (node.nodeType !== 1) return null;
    const element = node as Element;
    for (const attribute of ["data-refresh-key", "data-command-id", "data-action-id", "data-live-id", "data-project"]) {
      const value = element.getAttribute(attribute);
      if (value !== null) return attribute + ":" + value;
    }
    return null;
  };
  const compatible = (left: Node, right: Node): boolean =>
    left.nodeType === right.nodeType &&
    (left.nodeType !== 1 || (left as Element).tagName === (right as Element).tagName) &&
    key(left) === key(right);

  const patch = (target: Node, source: Node): void => {
    if (target.nodeType !== 1 || source.nodeType !== 1) {
      if (target.nodeValue !== source.nodeValue) target.nodeValue = source.nodeValue;
      return;
    }
    const before = target as Element;
    const after = source as Element;
    // Output and fetched details have their own refresh owners. A changed
    // revision (for example Git state) invalidates that island normally.
    if ((preserveRoot || before !== current) && before.hasAttribute("data-refresh-preserve") &&
        after.hasAttribute("data-refresh-preserve") &&
        before.getAttribute("data-refresh-revision") === after.getAttribute("data-refresh-revision")) return;
    const selection = before.tagName === "SELECT" ? (before as HTMLSelectElement).value : undefined;
    const preserveAttribute = (name: string): boolean => name === "open" && before.tagName === "DETAILS";
    for (const attribute of Array.from(before.attributes)) {
      if (!preserveAttribute(attribute.name) && !after.hasAttribute(attribute.name)) before.removeAttribute(attribute.name);
    }
    for (const attribute of Array.from(after.attributes)) {
      if (!preserveAttribute(attribute.name) && before.getAttribute(attribute.name) !== attribute.value) {
        before.setAttribute(attribute.name, attribute.value);
      }
    }

    const children = Array.from(before.childNodes);
    const keyed = new Map(children.flatMap((node) => key(node) === null ? [] : [[key(node)!, node] as const]));
    const retained = new Set<Node>();
    let cursor = before.firstChild;
    for (const next of Array.from(after.childNodes)) {
      const identity = key(next);
      let match: Node | undefined = identity === null
        ? children.find((node) => !retained.has(node) && key(node) === null && compatible(node, next))
        : keyed.get(identity);
      if (match && !compatible(match, next)) match = undefined;
      if (match) {
        retained.add(match);
        if (match !== cursor) before.insertBefore(match, cursor);
        patch(match, next);
      } else {
        match = next.cloneNode(true);
        before.insertBefore(match, cursor);
      }
      cursor = match.nextSibling;
    }
    for (const child of children) if (!retained.has(child)) child.remove();
    if (selection !== undefined && Array.from((before as HTMLSelectElement).options).some((option) => option.value === selection)) {
      (before as HTMLSelectElement).value = selection;
    }
  };
  patch(current, incoming);
}
