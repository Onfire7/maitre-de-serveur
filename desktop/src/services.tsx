// Catalog types and the Service spec built from an entry (decision 78). The
// Console's own panels are in src/app/; this file holds what they share.
import { create } from "@bufbuild/protobuf";
import { ServiceSpecSchema } from "./gen/maitre/v1/service_pb";
// Decision 78: the Catalog lives with the Console. This index is generated
// from the catalog/ submodule by `make gen`; the Node knows nothing of it.

// Mirrors catalog/FORMAT.md (the agent hands us index.json verbatim).
export interface CatalogEntry {
  id: string;
  name: string;
  summary: string;
  description: string;
  icon: string;
  category: string;
  image: string;
  version: string;
  exposure: string;
  own_login: boolean;
  infrastructure: boolean;
  host_ports?: string[];
  port_forward?: boolean;
  web?: { port: number; path: string; component?: string };
  volumes: { name: string; path: string; size?: string; shared?: boolean }[];
  settings?: { key: string; label: string; type: string; default?: string }[];
  health?: { http?: string };
  quadlet: string;
  /** A Service of several containers (plan C8): each with its own template; then image and quadlet are empty. */
  components?: { name: string; image: string; after?: string[]; wait_healthy?: boolean; egress?: boolean; quadlet: string }[];
  /** "internal": the Service's own network has no route out (plan C2). */
  network?: string;
  /** The bundle's own .volume and .network units. */
  units?: { name: string; body: string }[];
  /** Where a converted or imported entry came from. */
  source?: { from: string; url?: string; licence?: string; converted?: string; icon?: { pack: string; licence: string; url: string } };
  /** A converted entry's open questions (plan C10); it is not installed until there are none. */
  unfinished?: string[];
  /** The repository the app is developed in (docs/popularity-and-grants.md, P1). */
  project?: string;
  /** Its project's stars, from the Catalog's popularity.json: the popularity it sorts by (P2). */
  stars?: number;
  /** The user Catalog it came from (plan C9); none for the project's own. */
  catalog?: { id: string; name: string };
}

/** How a Service names the entry it came from: "jellyfin", or "<catalog>/<id>" for a user Catalog's. */
export function catalogKey(e: Pick<CatalogEntry, "id" | "catalog">): string {
  return e.catalog ? `${e.catalog.id}/${e.id}` : e.id;
}

/** A setting whose default is this is made up at install (plan C7). */
export const GENERATE = "generate";

/**
 * A password made up for a Service: 32 letters and digits, about 190 bits.
 * Letters and digits only, since apps such as Immich take nothing else in a
 * database password.
 */
export function generatePassword(): string {
  const abc = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789";
  const out: string[] = [];
  // Rejection sampling keeps every character equally likely.
  while (out.length < 32) {
    for (const b of crypto.getRandomValues(new Uint8Array(48))) if (b < 248 && out.length < 32) out.push(abc[b % 62]);
  }
  return out.join("");
}

/** The entry's settings that are made up at install, each with a fresh value. */
export function generatedSettings(e: CatalogEntry | null): Record<string, string> {
  return Object.fromEntries((e?.settings ?? []).filter((s) => s.default === GENERATE).map((s) => [s.key, generatePassword()]));
}
/** How the Catalog page orders entries (P5). */
export type CatalogSort = "popular" | "name";

/**
 * Most stars first, or by name. Entries without stars follow those with
 * them, by name, so a user Catalog without popularity.json still reads in
 * order.
 */
export function sortEntries<T extends Pick<CatalogEntry, "name" | "stars">>(entries: T[], by: CatalogSort): T[] {
  const byName = (a: T, b: T) => a.name.localeCompare(b.name);
  if (by === "name") return [...entries].sort(byName);
  return [...entries].sort((a, b) => (b.stars ?? -1) - (a.stars ?? -1) || byName(a, b));
}

/** Stars as people say them: 812, 8.1k, 81k. */
export function formatStars(n: number): string {
  if (n < 1000) return String(n);
  if (n < 10_000) return `${(Math.floor(n / 100) / 10).toString()}k`;
  return `${Math.floor(n / 1000)}k`;
}

export interface CatalogIndex {
  format: number;
  entries: CatalogEntry[];
}

export function specFor(e: CatalogEntry, name: string, settings: Record<string, string>) {
  return create(ServiceSpecSchema, {
    name: name || e.id,
    catalogId: catalogKey(e),
    image: e.image,
    settings: { ...Object.fromEntries((e.settings ?? []).map((s) => [s.key, s.default === GENERATE ? generatePassword() : (s.default ?? "")])), ...settings },
    exposure: e.exposure,
    quadlet: e.quadlet,
    infrastructure: e.infrastructure,
    hostPorts: e.host_ports ?? [],
    webPort: e.web?.port ?? 0,
    webPath: e.web?.path ?? "",
    healthHttp: e.health?.http ?? "",
    volumes: e.volumes.map((v) => ({ name: v.name, path: v.path, size: v.size ?? "", shared: !!v.shared })),
    components: (e.components ?? []).map((c) => ({ name: c.name, image: c.image, quadlet: c.quadlet, after: c.after ?? [], waitHealthy: !!c.wait_healthy, egress: !!c.egress })),
    webComponent: e.web?.component ?? "",
    units: e.units ?? [],
    networkInternal: e.network === "internal",
  });
}

/** Projects whose material the Catalog carries, as its NOTICE names them (plan C11, C12). */
const PROJECTS: Record<string, { name: string; url: string; licence: string; prefix?: string }> = {
  "dashboard-icons": { name: "dashboard-icons by Homarr Labs", url: "https://github.com/homarr-labs/dashboard-icons", licence: "Apache License 2.0" },
  "selfh.st/icons": { name: "selfh.st/icons", url: "https://selfh.st/icons", licence: "CC BY 4.0" },
  casaos: { name: "CasaOS AppStore by IceWhale Technology", url: "https://github.com/IceWhaleTech/CasaOS-AppStore", licence: "Apache License 2.0" },
  bigbear: { name: "Big Bear CasaOS, © 2023 Big Bear Enterprises, LLC", url: "https://github.com/bigbeartechworld/big-bear-casaos", licence: "MIT License" },
};

export interface Credit {
  name: string;
  url: string;
  licence: string;
  /** What of theirs the Catalog uses: "Icons of Immich, Jellyfin". */
  what: string;
}

/** The Credits the Console shows: each project whose icons or app definitions the Catalog uses, and for which entries. */
export function credits(index: CatalogIndex): Credit[] {
  const used = new Map<string, { icons: string[]; apps: string[] }>();
  const at = (k: string) => used.get(k) ?? (used.set(k, { icons: [], apps: [] }), used.get(k)!);
  for (const e of index.entries) {
    if (e.source?.icon) at(e.source.icon.pack).icons.push(e.name);
    const store = Object.entries(PROJECTS).find(([, p]) => e.source?.url?.startsWith(p.url + "/"))?.[0];
    if (store && (e.source?.from === "casaos" || e.source?.from === "compose")) at(store).apps.push(e.name);
  }
  return [...used].flatMap(([k, u]) => {
    const p = PROJECTS[k];
    if (!p) return [];
    const what = [u.icons.length && `Icons of ${u.icons.join(", ")}`, u.apps.length && `App definitions of ${u.apps.join(", ")}, converted`].filter(Boolean).join(". ");
    return [{ name: p.name, url: p.url, licence: p.licence, what }];
  });
}
