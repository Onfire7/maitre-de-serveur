// Storage protection as the Console reads it (ADR 0009): how each Space
// stands on each Node from the synced records and the gossiped status, and
// the most copies a Space can ask for (decision 259). Pure functions, so
// the Protection tab, the Storage tab's cards and the tests share them.
import type { FleetNodeStatus } from "@/gen/maitre/v1/node_pb";
import type { DriveRecord, Space } from "@/gen/maitre/v1/storage_pb";
import { spacesOf } from "@/spaces";

export type Word = "Protected" | "Partly protected" | "Not protected" | "Off" | "Held" | "Drive missing" | "Building parity" | "Rebuilding";

export interface Coverage {
  word: Word;
  why: string;
  /** Parity levels wanted and had on this Node, copies wanted and had. */
  levelWanted: number;
  levelGot: number;
  copiesWanted: number;
  copiesGot: number;
  drives: DriveRecord[];
}

/** The role a record says, "data" when it says none. */
export function roleOf(d: DriveRecord): "data" | "parity" | "mirrored" {
  return d.role === "parity" || d.role === "mirrored" ? d.role : "data";
}

/** The Drives of a Space on a Node that hold its files: not Parity Drives, not replaced ones. */
export function driversOf(space: Space, nodeId: string, drives: DriveRecord[], spaces: Space[]): DriveRecord[] {
  return drives.filter((d) => d.nodeId === nodeId && roleOf(d) !== "parity" && !d.replacedBy && spacesOf(d, spaces).includes(space.spaceId));
}

/** A Node's Parity Drives: what it gossips, else what the records say. */
export function parityDrivesOf(nodeId: string, drives: DriveRecord[], status?: FleetNodeStatus): string[] {
  if (status?.parity) return status.parity.parityDriveIds;
  return drives.filter((d) => d.nodeId === nodeId && roleOf(d) === "parity" && !d.replacedBy).map((d) => d.driveId);
}

/** How one Space is protected on one Node; undefined when the Space has no Drive there. */
export function coverage(space: Space, nodeId: string, spaces: Space[], drives: DriveRecord[], status: FleetNodeStatus | undefined, nodeName: string): Coverage | undefined {
  const mine = driversOf(space, nodeId, drives, spaces);
  if (mine.length === 0) return undefined;
  const parity = status?.parity;
  const copiesWanted = Math.max(1, space.copies);
  const cs = status?.copies.find((c) => c.spaceId === space.spaceId);
  const copiesGot = copiesWanted === 1 ? 1 : cs && cs.lastPassUnixMs ? (cs.shortFiles > 0 ? Math.min(copiesWanted - 1, cs.maxCopies || 1) : copiesWanted) : Math.min(copiesWanted, mine.length);
  const base = { levelWanted: space.mirrored ? 0 : space.parityLevel, levelGot: 0, copiesWanted, copiesGot, drives: mine };
  const missing = mine.filter((d) => parity?.missingDriveIds.includes(d.driveId));
  if (missing.length) return { ...base, word: "Drive missing", why: `${missing.map((d) => d.name).join(", ")} stopped answering.${space.parityLevel && !space.mirrored ? " Its files can be rebuilt onto a new disk from parity." : ""}` };
  if (space.mirrored) {
    const mirror = status?.mirrors.find((m) => mine.some((d) => d.driveId === m.driveId) && m.missingDisk);
    if (mirror) return { ...base, word: "Not protected", why: `${mine.find((d) => d.driveId === mirror.driveId)?.name} runs on one disk: ${mirror.missingDisk} is gone. Replace it soon.` };
    if (copiesGot < copiesWanted) return { ...base, word: "Partly protected", why: `mirrored; ${cs?.shortFiles} files have fewer than ${copiesWanted} copies` };
    return { ...base, word: "Protected", why: `mirrored: survives a failed disk${copiesWanted > 1 ? `, ${copiesWanted} copies` : ""}` };
  }
  const level = space.parityLevel;
  if (level === 0 && copiesWanted === 1) return { ...base, word: "Off", why: "One failed Drive loses its files" };
  const parityCount = parityDrivesOf(nodeId, drives, status).length;
  const levelGot = Math.min(level, parityCount);
  const withLevel = { ...base, levelGot };
  if (level > 0 && parity?.job === "fix") return { ...withLevel, word: "Rebuilding", why: `Rebuilding ${drives.find((d) => d.driveId === parity.jobDriveId)?.name ?? "a Drive"} from parity, ${Math.round(parity.progress * 100)}%` };
  if (level > 0 && levelGot > 0 && parity && !parity.lastSyncUnixMs) return { ...withLevel, word: "Building parity", why: parity.job === "sync" ? `First parity update, ${Math.round(parity.progress * 100)}%` : "Waiting for the first parity update" };
  if (level > 0 && levelGot > 0 && parity?.held) return { ...withLevel, word: "Held", why: parity.held };
  const shortParity = level > 0 && levelGot < level;
  const shortCopies = copiesGot < copiesWanted;
  if (!shortParity && !shortCopies) return { ...withLevel, word: "Protected", why: [level ? `survives ${level} failed Drive${level > 1 ? "s" : ""}` : "", copiesWanted > 1 ? `${copiesWanted} copies` : ""].filter(Boolean).join(", ") };
  const parts = [shortParity ? `parity ${levelGot} of ${level}${parityCount === 0 ? `: no Parity Drive on ${nodeName}` : ""}${space.spanNodes ? ". New files go to Nodes that have one" : ""}` : "", shortCopies ? `${cs?.shortFiles ?? "some"} files have fewer than ${copiesWanted} copies${cs && cs.maxCopies < copiesWanted ? `: ${cs.maxCopies} Drive${cs.maxCopies === 1 ? "" : "s"} on separate disks` : ""}` : ""].filter(Boolean);
  const got = levelGot + (copiesGot - 1);
  return { ...withLevel, word: got > 0 ? "Partly protected" : "Not protected", why: parts.join("; ") };
}

/**
 * The most copies a Space can ask for (decision 259): its Drives on
 * separate disks, across the Fleet for a Spanning Space, else on its
 * best-stocked Node. A Node gossips how many disks it counts; without
 * that, each Drive counts as one.
 */
export function maxCopies(space: Space, spaces: Space[], drives: DriveRecord[], status: Map<string, FleetNodeStatus>): number {
  const nodes = new Set(drives.map((d) => d.nodeId));
  let total = 0;
  let best = 0;
  for (const nodeId of nodes) {
    const mine = driversOf(space, nodeId, drives, spaces);
    if (mine.length === 0) continue;
    const gossiped = status.get(nodeId)?.copies.find((c) => c.spaceId === space.spaceId)?.maxCopies;
    const n = gossiped || mine.length;
    total += n;
    best = Math.max(best, n);
  }
  return Math.max(1, Math.min(3, space.spanNodes ? total : best));
}

export const PARITY_HELP =
  "Parity lets this Space's files survive a failed Drive, rebuilt from a Parity Drive. It is updated on a schedule, so files changed since the last update are not covered until the next one. It suits large files that rarely change: media, photos, documents. Turn it off for Spaces holding app data or databases. Those change constantly, are rarely covered, and each change weakens recovery for this Node's other Drives. Parity is not a backup: deleted or overwritten files are gone once the next update runs.";
export const COPIES_HELP =
  "Copies keeps each file on more than one Drive, up to the number of Drives the Space has. The Node watches for changes and copies a file soon after it is written, and checks every file each hour so nothing is missed. Two copies already survive one failed Drive. A file being written is not copied until it settles, so a database is better on a Mirrored Drive.";

export const DAY = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];

/** A schedule in words: "every night at 03:00, scrub every week". */
export function scheduleWords(s: { syncTime: string; syncDays: number[]; scrubEveryDays: number }): string {
  const time = s.syncTime || "03:00";
  const days = s.syncDays.length === 0 || s.syncDays.length === 7 ? "every night" : s.syncDays.length === 1 ? `on ${DAY[s.syncDays[0]]}` : `on ${[...s.syncDays].sort().map((d) => DAY[d]).join(", ")}`;
  const every = s.scrubEveryDays || 7;
  const scrub = every === 7 ? "every week" : every === 14 ? "every two weeks" : every % 7 === 0 ? `every ${every / 7} weeks` : `every ${every} days`;
  return `${days} at ${time}, scrub ${scrub}`;
}

/** The next sync after now, as the Node computes it (parity.NextSync). */
export function nextSync(s: { syncTime: string; syncDays: number[] }, now: Date): Date {
  const [hh, mm] = (s.syncTime || "03:00").split(":").map((x) => parseInt(x, 10));
  const days = new Set(s.syncDays);
  const t = new Date(now.getFullYear(), now.getMonth(), now.getDate(), isNaN(hh) ? 3 : hh, isNaN(mm) ? 0 : mm);
  for (let i = 0; i < 8; i++) {
    if (t > now && (days.size === 0 || days.has(t.getDay()))) return t;
    t.setDate(t.getDate() + 1);
  }
  return t;
}
