// @vitest-environment jsdom
import { beforeAll, describe, expect, it } from "vitest";
import { create } from "@bufbuild/protobuf";
import { RecordSchema } from "@/gen/maitre/v1/fleet_pb";

beforeAll(() => localStorage.setItem("maitre.demo", "1"));

describe("signedForProvision", () => {
  it("signs records from before decision 189 and leaves signed ones alone", async () => {
    const { signedForProvision } = await import("./sign");
    const signed = create(RecordSchema, { kind: "space", id: "general", body: new Uint8Array([1]), signerKey: new Uint8Array(32).fill(7), signature: new Uint8Array(64).fill(9), signedUnixMs: 5n });
    const legacy = create(RecordSchema, { kind: "tailscale", id: "fleet", body: new Uint8Array([2, 3]), signedUnixMs: 1_700_000_000_000n });
    const out = await signedForProvision([signed, legacy]);
    expect(out[0]).toBe(signed);
    expect(out[1].kind).toBe("tailscale");
    expect(out[1].body).toEqual(new Uint8Array([2, 3]));
    expect(out[1].signature.length).toBe(64);
    expect(out[1].signedUnixMs > legacy.signedUnixMs).toBe(true);
  });
});
