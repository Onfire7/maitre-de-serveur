import { createClient } from "@connectrpc/connect";
import { FilesService } from "@/gen/maitre/v1/files_pb";
import type { Space } from "@/gen/maitre/v1/storage_pb";
import type { ServiceSpec } from "@/gen/maitre/v1/service_pb";
import type { FleetSnapshot } from "@/fleet/useFleet";
import { targetFor } from "@/fleet/actions";
import { spacesOf } from "@/spaces";
import { transportFor } from "@/transport";
import type { FolderSource } from "./FolderBrowser";

/** Browse and make folders of a Space's Pool on one Node. */
export function folderSource(snap: FleetSnapshot | undefined, nodeId: string, spaceId: string): FolderSource {
  const client = () => {
    const t = targetFor(snap, [], nodeId);
    if (!t) throw new Error("this Node has no known address");
    return createClient(FilesService, transportFor(t));
  };
  return {
    key: `${nodeId}:${spaceId}`,
    list: async (path) => (await client().listDir({ scope: { spaceId, driveId: "" }, path })).entries.filter((e) => e.dir).map((e) => e.name).sort((a, b) => a.localeCompare(b)),
    make: async (path) => {
      await client().makeDir({ scope: { spaceId, driveId: "" }, path });
    },
  };
}

/** Spaces a Service on this Node can use (decision 176): spanning ones and
 * those with a Drive on the Node. */
export function spacesOn(snap: FleetSnapshot | undefined, nodeId: string): Space[] {
  const spaces = snap?.spaces ?? [];
  return spaces.filter((sp) => sp.spanNodes || (snap?.drives ?? []).some((d) => d.nodeId === nodeId && spacesOf(d, spaces).includes(sp.spaceId)));
}

/** One folder a Service uses: a volume (its own folder or a shared one) or
 * an extra folder, where it is in its Space, and where the container sees
 * it. `key` is the volume's name, or `mount:` and the container path. */
export interface ServiceFolder {
  key: string;
  label: string;
  inContainer: string;
  spaceId: string;
  path: string;
  shared: boolean;
  readOnly: boolean;
}

/** Every folder a Service has, volumes first (decision 195). */
export function serviceFolders(spec: ServiceSpec): ServiceFolder[] {
  const vols = spec.volumes.map((v) => {
    const shared = v.name in spec.volumeFolders;
    return { key: v.name, label: v.name, inContainer: v.path, spaceId: spec.volumeSpaces[v.name] ?? "", path: shared ? spec.volumeFolders[v.name] : `${spec.name}/${v.name}`, shared, readOnly: false };
  });
  const mounts = spec.mounts.map((m) => ({ key: `mount:${m.path}`, label: m.path.split("/").filter(Boolean).pop() ?? m.path, inContainer: m.path, spaceId: m.spaceId, path: m.folder, shared: true, readOnly: m.readOnly }));
  return [...vols, ...mounts];
}
