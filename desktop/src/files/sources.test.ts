import { describe, expect, it } from "vitest";
import { create } from "@bufbuild/protobuf";
import { ServiceSpecSchema } from "@/gen/maitre/v1/service_pb";
import { seenBy, serviceFolders, serviceTargets } from "./sources";

describe("serviceFolders (decision 195)", () => {
  it("lists a Service's own and shared volumes, then its extra folders, where they really are", () => {
    const spec = create(ServiceSpecSchema, {
      name: "jellyfin",
      volumes: [
        { name: "config", path: "/config" },
        { name: "media", path: "/media" },
      ],
      volumeSpaces: { config: "general", media: "media" },
      volumeFolders: { media: "" },
      mounts: [{ spaceId: "media", folder: "Music/Live", path: "/mnt/live", readOnly: true }],
    });
    expect(serviceFolders(spec).map((f) => [f.key, f.spaceId, f.path, f.shared, f.readOnly])).toEqual([
      ["config", "general", ".Services/jellyfin/config", false, false],
      ["media", "media", "", true, false],
      ["mount:/mnt/live", "media", "Music/Live", true, true],
    ]);
  });
});

describe("serviceTargets and seenBy (decision 216)", () => {
  const jellyfin = create(ServiceSpecSchema, {
    name: "jellyfin",
    nodeId: "a",
    volumes: [
      { name: "config", path: "/config" },
      { name: "media", path: "/media" },
    ],
    volumeSpaces: { config: "general", media: "media" },
    volumeFolders: { media: "" },
    mounts: [{ spaceId: "media", folder: "Music", path: "/music", readOnly: true }],
  });
  const kuma = create(ServiceSpecSchema, { name: "kuma", nodeId: "b", volumes: [{ name: "data", path: "/app/data" }], volumeSpaces: { data: "general" } });
  const targets = serviceTargets([kuma, jellyfin]);

  it("lists the folders files can go into, leaving out read-only ones", () => {
    expect(targets.map((t) => [t.service, t.nodeId, t.folder.key])).toEqual([
      ["jellyfin", "a", "config"],
      ["jellyfin", "a", "media"],
      ["kuma", "b", "data"],
    ]);
  });

  it("says where a folder shows up in a container", () => {
    expect(seenBy(targets, "general", ".Services/jellyfin/config/plugins")).toEqual([{ service: "jellyfin", inContainer: "/config/plugins" }]);
    expect(seenBy(targets, "media", "Movies")).toEqual([{ service: "jellyfin", inContainer: "/media/Movies" }]);
    expect(seenBy(targets, "media", "")).toEqual([{ service: "jellyfin", inContainer: "/media" }]);
    expect(seenBy(targets, "general", "Documents")).toEqual([]);
  });
});
