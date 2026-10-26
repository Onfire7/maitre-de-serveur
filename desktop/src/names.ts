// Node names: short, friendly, easy to say out loud across a room. A new
// Node gets one of these by default; the dice button draws another.
const NODE_NAMES = [
  "Alder", "Ash", "Aspen", "Beech", "Birch", "Cedar", "Cypress", "Elm", "Fir", "Hazel", "Juniper", "Larch", "Linden", "Magnolia", "Maple", "Oak", "Olive", "Pine", "Poplar", "Rowan", "Sequoia", "Spruce", "Willow", "Yew",
  "Amber", "Basalt", "Cobalt", "Flint", "Garnet", "Granite", "Jade", "Jasper", "Marble", "Obsidian", "Onyx", "Opal", "Quartz", "Slate", "Topaz",
  "Comet", "Delta", "Echo", "Ember", "Harbor", "Horizon", "Lantern", "Meadow", "Meridian", "Nimbus", "Orbit", "Pebble", "Prairie", "Quill", "Ridge", "River", "Summit", "Tundra", "Vesper", "Zenith",
];

/** A name not already used in the Fleet (falls back to a numbered one when all are taken). */
export function randomNodeName(taken: Iterable<string>): string {
  const used = new Set([...taken].map((n) => n.trim().toLowerCase()));
  const free = NODE_NAMES.filter((n) => !used.has(n.toLowerCase()));
  if (free.length === 0) return `Node ${used.size + 1}`;
  return free[Math.floor(Math.random() * free.length)];
}
