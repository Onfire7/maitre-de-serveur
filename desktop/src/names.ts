// Node names follow the Node's letter (decision 104): Node A is Alder or
// Avalon, its Drives Apollo or Atlas. Short, friendly, easy to say across a
// room, and disjoint from the Drive lists in server/internal/storage/names.go.
const NODE_NAMES: Record<string, string[]> = {
  A: ["Alder", "Acacia", "Azalea", "Anvil", "Avalon"],
  B: ["Bayou", "Boulder", "Briar", "Bastion", "Bluff"],
  C: ["Cypress", "Cascade", "Cinder", "Cove", "Crescent"],
  D: ["Dover", "Dapple", "Dory", "Dakota", "Dawn"],
  E: ["Ellis", "Ebony", "Eden", "Estuary", "Elder"],
  F: ["Fennel", "Foxglove", "Forge", "Fathom", "Fiddler"],
  G: ["Ginkgo", "Gull", "Gossamer", "Gorge", "Gable"],
  H: ["Hickory", "Haven", "Hearth", "Hummock", "Holt"],
  I: ["Inlet", "Ibis", "Isle", "Ironwood", "Ivy"],
  J: ["Jonquil", "Juneau", "Jericho", "Jigsaw", "Jute"],
  K: ["Kodiak", "Kinship", "Kerry", "Knoll", "Kona"],
  L: ["Laurel", "Loft", "Linden", "Lichen", "Lido"],
  M: ["Marsh", "Mallow", "Moss", "Myrtle", "Mirth"],
  N: ["Nook", "Nettle", "Navarre", "Nautilus", "Noble"],
  O: ["Orchard", "Osprey", "Olive", "Ochre", "Outpost"],
  P: ["Poplar", "Pampas", "Paddock", "Pinto", "Plume"],
  Q: ["Quay", "Quetzal", "Quokka", "Quarry", "Quinoa"],
  R: ["Rowan", "Rookery", "Rill", "Rosewood", "Rambler"],
  S: ["Sorrel", "Sycamore", "Saffron", "Shoal", "Sable"],
  T: ["Tamarack", "Teal", "Thicket", "Terrace", "Tansy"],
  U: ["Upton", "Ulster", "Ulm", "Unwin", "Uxbridge"],
  V: ["Verdant", "Vale", "Vireo", "Vermilion", "Vista"],
  W: ["Wicker", "Wharf", "Wildwood", "Warbler", "Weir"],
  X: ["Xylo", "Xanthe", "Xander", "Xiomara", "Xola"],
  Y: ["Yule", "Yorke", "Yarn", "Yaffle", "Yarmouth"],
  Z: ["Zelda", "Zion", "Zorro", "Zuma", "Zest"],
};

const LETTERS = "ABCDEFGHIJKLMNOPQRSTUVWXYZ";

/** The letter a new Node will take: the first one no Node has (the agent applies the same rule). */
export function nextLetter(usedLetters: Iterable<string>): string {
  const used = new Set([...usedLetters].map((l) => l.toUpperCase()));
  for (const l of LETTERS) if (!used.has(l)) return l;
  return "";
}

/** A name starting with letter that no Node uses yet; "<Letter>-node-N" when the list is spent. */
export function randomNodeName(letter: string, taken: Iterable<string>): string {
  const L = (letter || "A").toUpperCase();
  const used = new Set([...taken].map((n) => n.trim().toLowerCase()));
  const free = (NODE_NAMES[L] ?? []).filter((n) => !used.has(n.toLowerCase()));
  if (free.length === 0) return `${L}-node-${used.size + 1}`;
  return free[Math.floor(Math.random() * free.length)];
}
