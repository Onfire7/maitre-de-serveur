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
  web?: { port: number; path: string };
  volumes: { name: string; path: string; size?: string }[];
  settings?: { key: string; label: string; type: string; default?: string }[];
  health?: { http?: string };
  quadlet: string;
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
    settings: { ...Object.fromEntries((e.settings ?? []).map((s) => [s.key, s.default ?? ""])), ...settings },
    exposure: e.exposure,
    quadlet: e.quadlet,
    infrastructure: e.infrastructure,
    hostPorts: e.host_ports ?? [],
    webPort: e.web?.port ?? 0,
    webPath: e.web?.path ?? "",
    healthHttp: e.health?.http ?? "",
    volumes: e.volumes.map((v) => ({ name: v.name, path: v.path, size: v.size ?? "" })),
  });
}
