// Anchor choice (decisions 159 and 284): the Console reads the whole Fleet
// through one reachable Node, trying every way to each Node in turn: the
// last way that answered first, then the user's pin, then each Node by
// letter with its addresses in order (at home, Tailscale, the domain).
export interface Candidate {
  nodeId: string;
  letter: string;
  addresses: string[];
}

export interface AnchorPick {
  nodeId: string;
  addr: string;
}

/** The key a failed attempt is remembered under. */
export function attemptKey(p: AnchorPick): string {
  return `${p.nodeId}@${p.addr}`;
}

/** Every way to every Node, in the order to try them. */
export function allPicks(nodes: Candidate[]): AnchorPick[] {
  const byLetter = [...nodes].sort((a, b) => (a.letter || "~").localeCompare(b.letter || "~") || a.nodeId.localeCompare(b.nodeId));
  return byLetter.flatMap((n) => n.addresses.map((addr) => ({ nodeId: n.nodeId, addr })));
}

/**
 * Pick the Node and address to read the Fleet through. `failed` holds the
 * attempts (by attemptKey) that did not answer this round, so a failover
 * skips them; the same Node's other addresses come before the next Node.
 * When every way failed the pick starts over from the top so the next
 * round retries.
 */
export function pickAnchor(nodes: Candidate[], last: AnchorPick | undefined, failed: ReadonlySet<string> = new Set()): AnchorPick | undefined {
  const all = allPicks(nodes);
  if (all.length === 0) return undefined;
  const known = (p: AnchorPick) => all.some((x) => x.nodeId === p.nodeId && x.addr === p.addr);
  if (last && known(last) && !failed.has(attemptKey(last))) return last;
  const open = all.filter((p) => !failed.has(attemptKey(p)));
  if (open.length === 0) return all[0];
  // Stay on the last Node while it has ways left to try.
  if (last) {
    const same = open.find((p) => p.nodeId === last.nodeId);
    if (same) return same;
  }
  return open[0];
}
