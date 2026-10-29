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
  source?: { from: string; url?: string; licence?: string; converted?: string };
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
export interface CatalogIndex {
  format: number;
  entries: CatalogEntry[];
}

export function specFor(e: CatalogEntry, name: string, settings: Record<string, string>) {
  return create(ServiceSpecSchema, {
    name: name || e.id,
    catalogId: e.id,
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
