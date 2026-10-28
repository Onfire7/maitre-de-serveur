import { describe, expect, it } from "vitest";
import { create } from "@bufbuild/protobuf";
import { SpaceSchema } from "@/gen/maitre/v1/storage_pb";
import { fillOf, withFill } from "./StorageTab";

describe("a Space's fill and Prefer local Drives (decision 202)", () => {
  it("reads local_first as most free with local preferred, and writes it back so older Nodes still know it", () => {
    const old = create(SpaceSchema, { spaceId: "media", name: "Media", placement: "local_first" });
    expect(fillOf(old)).toEqual({ fill: "most_free", preferLocal: true });
    expect(withFill(old, "most_free", true)).toMatchObject({ placement: "local_first", preferLocal: true });
    expect(withFill(old, "most_free", false)).toMatchObject({ placement: "most_free", preferLocal: false });
    const least = withFill(old, "least_free_percent", true);
    expect(least).toMatchObject({ placement: "least_free_percent", preferLocal: true });
    expect(fillOf(least)).toEqual({ fill: "least_free_percent", preferLocal: true });
  });
});
