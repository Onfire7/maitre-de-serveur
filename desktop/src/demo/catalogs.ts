// The demo Fleet's user Catalogs (plan C9): what the bundled helper would
// read from a git Catalog and a folder on this computer, and what
// `maitre catalog add` would say of a Compose file added to the folder.
import type { CatalogEntry, CatalogIndex } from "@/services";

const pin = (image: string, d: string) => `${image}@sha256:${d.repeat(32)}`;
const hand = { from: "hand" };

function single(id: string, name: string, summary: string, image: string, port: number, extra: Partial<CatalogEntry> = {}): CatalogEntry {
  return {
    id, name, summary, description: "", icon: "", category: "tools", image, version: image.split(":")[1]?.split("@")[0] ?? "", exposure: "private",
    own_login: false, infrastructure: false, web: { port, path: "/" }, volumes: [], settings: [],
    quadlet: "[Container]\nImage={{ .Image }}\nContainerName={{ .Name }}\nNetwork={{ .Network }}\n", source: hand, ...extra,
  };
}

/** The community Catalog, as its clone holds it: two entries and one that does not load. */
export const communityEntries = (): CatalogEntry[] => [
  single("it-tools", "IT Tools", "Handy converters and generators for developers, in the browser.", pin("docker.io/corentinth/it-tools:2024.10.22-7ca5933", "a1"), 80),
  single("linkding", "linkding", "A minimal bookmark manager that saves pages to read later.", pin("docker.io/sissbruecker/linkding:1.44.1", "b2"), 9090, {
    own_login: true,
    volumes: [{ name: "data", path: "/etc/linkding/data" }],
    quadlet: "[Container]\nImage={{ .Image }}\nContainerName={{ .Name }}\nNetwork={{ .Network }}\nVolume={{ .Volumes.data }}:/etc/linkding/data:Z\n",
  }),
];

let mine: CatalogEntry[] = [
  single("whoami", "whoami", "Answers with the request it was sent: for testing the network.", pin("docker.io/traefik/whoami:v1.11", "c3"), 80),
];

export const MY_FOLDER = "/home/sean/maitre-catalog";

export function demoCatalogLoad(source: { id: string; folder: string; git_url: string }): { index: CatalogIndex & { problems?: { entry: string; error: string }[] }; commit: string; dir: string } {
  if (source.git_url) {
    return {
      index: { format: 1, entries: communityEntries(), problems: [{ entry: "homepage", error: "image must be pinned by @sha256 digest" }] },
      commit: "4f2c9e1b7a03d5e8c6b1f09a2d4e7c3b5a8f1e20",
      dir: `/demo/catalogs/${source.id}`,
    };
  }
  if (source.folder === MY_FOLDER) return { index: { format: 1, entries: mine }, commit: "", dir: MY_FOLDER };
  throw new Error(`${source.folder} is not a folder on this computer`);
}

/** `maitre catalog add --json`, as it would answer for any Compose file: Uptime Kuma's own, here. */
export function demoCatalogAdd(folder: string, compose: string) {
  if (folder !== MY_FOLDER) throw new Error(`${folder} is not a folder on this computer`);
  const entry = single("uptime-kuma", "Uptime Kuma", "Uptime Kuma, converted from a Compose file.", pin("docker.io/louislam/uptime-kuma:2.0.2", "d4"), 3001, {
    volumes: [{ name: "data", path: "/app/data" }],
    quadlet: "[Container]\nImage={{ .Image }}\nContainerName={{ .Name }}\nNetwork={{ .Network }}\nVolume={{ .Volumes.data }}:/app/data:Z\n",
    source: { from: "compose", url: compose, converted: "2026-10-29" },
    unfinished: ["Write the entry's summary and description", "Say under which licence the Compose file was published (source.licence)"],
  });
  mine = [...mine.filter((e) => e.id !== entry.id), entry];
  return {
    report: {
      needs_person: entry.unfinished,
      changed: ["uptime-kuma: the folder ./uptime-kuma is the Service volume \"data\", kept in a Storage Space", "restart: policies are replaced by Maître's: each container restarts when it fails"],
    },
    entry,
    dir: `${folder}/entries/uptime-kuma`,
  };
}
