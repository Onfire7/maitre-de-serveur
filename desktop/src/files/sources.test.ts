import { describe, expect, it } from "vitest";
import { create } from "@bufbuild/protobuf";
import { ServiceSpecSchema } from "@/gen/maitre/v1/service_pb";
import { serviceFolders } from "./sources";

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
      ["config", "general", "jellyfin/config", false, false],
      ["media", "media", "", true, false],
      ["mount:/mnt/live", "media", "Music/Live", true, true],
    ]);
  });
});
