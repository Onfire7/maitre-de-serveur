// Nodes and Drives share one name list per letter: Node A is Alder or
// Apollo, and its Drives take the other A names. The list lives in
// server/internal/storage/names.go; `make gen` exports it to names.json.
import lists from "./names.json";

const NAMES = lists as Record<string, string[]>;
const LETTERS = "ABCDEFGHIJKLMNOPQRSTUVWXYZ";

/** The letter a new Node will take: the first one no Node has (the agent applies the same rule). */
export function nextLetter(usedLetters: Iterable<string>): string {
  const used = new Set([...usedLetters].map((l) => l.toUpperCase()));
  for (const l of LETTERS) if (!used.has(l)) return l;
  return "";
}

/** A random name starting with letter that neither a Node nor a Drive uses; "<Name> 2" style when the list is spent. */
export function randomNodeName(letter: string, taken: Iterable<string>): string {
  const L = (letter || "A").toUpperCase();
  const used = new Set([...taken].map((n) => n.trim().toLowerCase()));
  const list = NAMES[L] ?? [];
  const free = list.filter((n) => !used.has(n.toLowerCase()));
  if (free.length > 0) return free[Math.floor(Math.random() * free.length)];
  for (let round = 2; ; round++) {
    const again = list.map((n) => `${n} ${round}`).filter((n) => !used.has(n.toLowerCase()));
    if (again.length > 0) return again[Math.floor(Math.random() * again.length)];
  }
}
