// User Catalogs (plan C9, docs/services-from-compose.md): the Fleet's list
// of Catalogs is one signed record (kind "catalogs", id "fleet"); each
// Console reads the entries itself through the bundled maitre helper, a
// folder where it is on this computer, a git repository from its own clone.
// What changed in each since this Console last looked is kept here, per
// Catalog, in localStorage.
import { useQuery } from "@tanstack/react-query";
import { invoke } from "@tauri-apps/api/core";
import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import { UserCatalogsSchema, type UserCatalog } from "@/gen/maitre/v1/service_pb";
import type { Record as Rec } from "@/gen/maitre/v1/fleet_pb";
import type { CatalogEntry, CatalogIndex } from "@/services";
import type { Target } from "@/transport";
import { currentRecord, putSigned } from "./sign";

export const CATALOGS_KIND = "catalogs";

/** The Fleet's Catalogs, from its records. */
export function fleetCatalogs(records: Rec[] | undefined): UserCatalog[] {
  const r = currentRecord(records, CATALOGS_KIND, "fleet");
  if (!r || r.deleted) return [];
  try {
    return fromBinary(UserCatalogsSchema, r.body).catalogs;
  } catch {
    return [];
  }
}

/** Save the Fleet's list of Catalogs, signed by this device. */
export async function saveCatalogs(t: Target, records: Rec[] | undefined, catalogs: UserCatalog[]) {
  const body = toBinary(UserCatalogsSchema, create(UserCatalogsSchema, { catalogs }));
  await putSigned(t, { kind: CATALOGS_KIND, id: "fleet", body }, currentRecord(records, CATALOGS_KIND, "fleet"));
}

/** A Catalog id from its name: lowercase letters, digits and dashes, unused. */
export function catalogId(name: string, taken: string[]): string {
  const base = name.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-+|-+$/g, "").slice(0, 32) || "catalog";
  let id = base;
  for (let i = 2; taken.includes(id) || id === "maitre"; i++) id = `${base}-${i}`;
  return id;
}

export interface Problem {
  entry: string;
  error: string;
}

/** One Catalog as this Console read it. */
export interface LoadedCatalog {
  catalog: UserCatalog;
  entries: CatalogEntry[];
  problems: Problem[];
  /** The commit a git Catalog is at. */
  commit: string;
  /** Where it was read on this computer. */
  dir: string;
  /** Why it could not be read, if it could not. */
  error?: string;
  /** A folder on another computer: listed, not read. */
  elsewhere?: boolean;
}

/** Whether this computer holds a folder Catalog (and so can add to it). */
export function onThisComputer(c: UserCatalog, deviceId: string): boolean {
  return !!c.folder && c.folderDevice === deviceId;
}

/** Read one Catalog; its entries carry which Catalog they came from. */
export async function loadCatalog(c: UserCatalog, deviceId: string): Promise<LoadedCatalog> {
  const base = { catalog: c, entries: [], problems: [], commit: "", dir: "" };
  if (c.folder && !onThisComputer(c, deviceId)) return { ...base, elsewhere: true };
  try {
    const res = await invoke<{ index: CatalogIndex & { problems?: Problem[] }; commit: string; dir: string }>("catalog_load", {
      source: { id: c.id, folder: c.folder, git_url: c.gitUrl, git_branch: c.gitBranch },
    });
    const entries = res.index.entries.map((e) => ({ ...e, catalog: { id: c.id, name: c.name } }));
    return { ...base, entries, problems: res.index.problems ?? [], commit: res.commit, dir: res.dir };
  } catch (e) {
    return { ...base, error: String((e as Error)?.message ?? e) };
  }
}

/** Every Catalog of the Fleet, read; refreshed when the Catalog page asks. */
export function useCatalogs(records: Rec[] | undefined, deviceId: string | undefined) {
  const catalogs = fleetCatalogs(records);
  return useQuery({
    queryKey: ["catalogs", deviceId, catalogs.map((c) => `${c.id}:${c.folder}:${c.gitUrl}:${c.gitBranch}`).join(",")],
    enabled: !!deviceId,
    staleTime: 5 * 60_000,
    queryFn: () => Promise.all(catalogs.map((c) => loadCatalog(c, deviceId!))),
  });
}

// --- what changed since this Console last looked ---------------------------

/** An entry as compared between looks: everything but its (large) icon. */
function comparable(e: CatalogEntry): string {
  const { icon: _icon, catalog: _catalog, ...rest } = e;
  return JSON.stringify(rest, null, 2);
}

const seenKey = (id: string) => `maitre.catalogSeen.${id}`;

/** The entries of a Catalog as this Console last saw them, by id; undefined if never looked at. */
export function lastSeen(id: string): Record<string, string> | undefined {
  try {
    const raw = localStorage.getItem(seenKey(id));
    return raw ? JSON.parse(raw) : undefined;
  } catch {
    return undefined;
  }
}

export function markSeen(c: LoadedCatalog) {
  localStorage.setItem(seenKey(c.catalog.id), JSON.stringify(Object.fromEntries(c.entries.map((e) => [e.id, comparable(e)]))));
}

export interface Changes {
  added: CatalogEntry[];
  changed: { entry: CatalogEntry; diff: DiffLine[] }[];
  removed: string[];
}

/**
 * What changed in a Catalog since it was last seen. A Catalog never looked
 * at has no changes: everything in it is simply there.
 */
export function changesSince(c: LoadedCatalog, seen = lastSeen(c.catalog.id)): Changes {
  const none = { added: [], changed: [], removed: [] };
  if (!seen || c.error || c.elsewhere) return none;
  const now = new Map(c.entries.map((e) => [e.id, e]));
  return {
    added: c.entries.filter((e) => !(e.id in seen)),
    changed: c.entries.filter((e) => e.id in seen && seen[e.id] !== comparable(e)).map((e) => ({ entry: e, diff: diffLines(seen[e.id], comparable(e)) })),
    removed: Object.keys(seen).filter((id) => !now.has(id)),
  };
}

export function changeCount(ch: Changes): number {
  return ch.added.length + ch.changed.length + ch.removed.length;
}

export interface DiffLine {
  kind: " " | "+" | "-";
  text: string;
}

/** A line diff (longest common subsequence), with only changed lines and two of context. */
export function diffLines(before: string, after: string): DiffLine[] {
  const a = before.split("\n");
  const b = after.split("\n");
  const n = a.length;
  const m = b.length;
  const lcs = Array.from({ length: n + 1 }, () => new Array<number>(m + 1).fill(0));
  for (let i = n - 1; i >= 0; i--) for (let j = m - 1; j >= 0; j--) lcs[i][j] = a[i] === b[j] ? lcs[i + 1][j + 1] + 1 : Math.max(lcs[i + 1][j], lcs[i][j + 1]);
  const all: DiffLine[] = [];
  let i = 0;
  let j = 0;
  while (i < n || j < m) {
    if (i < n && j < m && a[i] === b[j]) (all.push({ kind: " ", text: a[i] }), i++, j++);
    else if (i < n && (j === m || lcs[i + 1][j] >= lcs[i][j + 1])) (all.push({ kind: "-", text: a[i] }), i++);
    else (all.push({ kind: "+", text: b[j] }), j++);
  }
  const near = (k: number) => all.slice(Math.max(0, k - 2), k + 3).some((l) => l.kind !== " ");
  return all.filter((l, k) => l.kind !== " " || near(k));
}
