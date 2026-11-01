// Decision 276: the Service and Drive records of Nodes removed from the Fleet.
import { describe, expect, it } from "vitest";
import { create, toBinary } from "@bufbuild/protobuf";
import { RecordSchema, type Record as Rec } from "@/gen/maitre/v1/fleet_pb";
import { ServiceSpecSchema } from "@/gen/maitre/v1/service_pb";
import { DriveRecordSchema } from "@/gen/maitre/v1/storage_pb";
import { leftBehind, leftBy } from "./leftovers";

const A = "a".repeat(64);
const GONE = "4".repeat(64);
const NEVER = "9".repeat(64);

function rec(kind: string, id: string, owner: string, body: Uint8Array = new Uint8Array(), deleted = false): Rec {
  return create(RecordSchema, { kind, id, ownerNodeId: owner, version: 1n, body, deleted });
}
const svc = (name: string, nodeId: string) => toBinary(ServiceSpecSchema, create(ServiceSpecSchema, { name, nodeId }));
const drv = (name: string, nodeId: string) => toBinary(DriveRecordSchema, create(DriveRecordSchema, { driveId: "d", nodeId, name }));

const records: Rec[] = [
  rec("node", A, A, new Uint8Array([1])),
  rec("node", GONE, GONE, new Uint8Array(), true),
  rec("service", "jellyfin", GONE, svc("jellyfin", GONE)),
  rec("service", "uptime-kuma", GONE, svc("uptime-kuma", GONE), true),
  rec("service", "web", A, svc("web", A)),
  rec("drive", "edee736a", GONE, drv("Old disk", GONE)),
  rec("drive", "6f6dc847", A, drv("Boulder SSD", A)),
  rec("drive", "x", NEVER, drv("Mystery", NEVER)),
  rec("bin", "default", GONE, new Uint8Array(), true),
];

describe("leftBehind", () => {
  it("lists live Services and Drives of removed or unknown Nodes, Drives first then by name", () => {
    expect(leftBehind(records).map((l) => `${l.kind} ${l.name} of ${l.nodeId.slice(0, 2)}`)).toEqual(["drive Mystery of 99", "drive Old disk of 44", "service jellyfin of 44"]);
  });
  it("keeps the record to sign over", () => {
    expect(leftBehind(records).find((l) => l.name === "jellyfin")?.rec.id).toBe("jellyfin");
  });
});

describe("leftBy", () => {
  it("is what removing a live Node would leave", () => {
    expect(leftBy(records, A).map((l) => l.id)).toEqual(["6f6dc847", "web"]);
    expect(leftBy(records, GONE).map((l) => l.id)).toEqual(["edee736a", "jellyfin"]);
  });
});
