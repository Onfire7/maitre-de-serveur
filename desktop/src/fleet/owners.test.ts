import { describe, expect, it } from "vitest";
import { create } from "@bufbuild/protobuf";
import { SpaceSchema } from "@/gen/maitre/v1/storage_pb";
import { nextOwnerId, ownersToAssign } from "./owners";

describe("Space owners (decision 182)", () => {
  it("start at 61000 and never reuse an id", () => {
    expect(nextOwnerId([])).toBe(61000);
    const spaces = [create(SpaceSchema, { spaceId: "media", ownerId: 61004, createdUnixMs: 5n }), create(SpaceSchema, { spaceId: "b", createdUnixMs: 9n }), create(SpaceSchema, { spaceId: "a", createdUnixMs: 2n })];
    expect(nextOwnerId(spaces)).toBe(61005);
    // Oldest first, after the highest id in use.
    expect(ownersToAssign(spaces).map((s) => [s.spaceId, s.ownerId])).toEqual([
      ["a", 61005],
      ["b", 61006],
    ]);
  });
});
