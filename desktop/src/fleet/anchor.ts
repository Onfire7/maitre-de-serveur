// Anchor choice (decision 159): the Console reads the whole Fleet through one
// reachable Node. Last used first, then the first Healthy Node by letter.
export interface Candidate {
  nodeId: string;
  letter: string;
  addresses: string[];
}

export interface AnchorPick {
  nodeId: string;
  addr: string;
}

/**
 * Pick the Node to read the Fleet through. `failed` holds the Nodes (by id)
 * that did not answer this round, so a failover skips them; when every Node
 * failed the pick starts over from the top so the next round retries.
 */
export function pickAnchor(nodes: Candidate[], last: AnchorPick | undefined, failed: ReadonlySet<string> = new Set()): AnchorPick | undefined {
  const usable = nodes.filter((n) => n.addresses.length > 0);
  if (usable.length === 0) return undefined;
  if (last && !failed.has(last.nodeId)) {
    const same = usable.find((n) => n.nodeId === last.nodeId);
    if (same) return { nodeId: same.nodeId, addr: same.addresses.includes(last.addr) ? last.addr : same.addresses[0] };
  }
  const byLetter = [...usable].sort((a, b) => (a.letter || "~").localeCompare(b.letter || "~") || a.nodeId.localeCompare(b.nodeId));
  const next = byLetter.find((n) => !failed.has(n.nodeId)) ?? byLetter[0];
  return { nodeId: next.nodeId, addr: next.addresses[0] };
}
