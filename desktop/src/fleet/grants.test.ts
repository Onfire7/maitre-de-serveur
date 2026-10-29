import { describe, expect, it, vi, beforeEach } from "vitest";
import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import { createHash } from "node:crypto";

const invoke = vi.fn();
vi.mock("@tauri-apps/api/core", () => ({ invoke: (...args: unknown[]) => invoke(...args) }));

import { GrantSchema, GrantsNeededSchema, RecordSchema } from "@/gen/maitre/v1/fleet_pb";
import { ListRecordsResponseSchema, PutRecordRequestSchema, PutRecordResponseSchema } from "@/gen/maitre/v1/node_pb";
import { fingerprintOfPem, nextSignedMs, signedRecord } from "./sign";
import { GrantsDeclined, optionNote, setGrantAsker, withGrants } from "./grants";
import type { CatalogEntry } from "@/services";

const T = { addr: "10.0.0.5:7443", fingerprint: "a1".repeat(32) };

describe("signed records (decision 189)", () => {
  beforeEach(() => {
    invoke.mockReset();
  });

  it("are signed later than the record they replace", () => {
    expect(nextSignedMs(undefined, 1000)).toBe(1000n);
    expect(nextSignedMs(create(RecordSchema, { signedUnixMs: 500n }), 1000)).toBe(1000n);
    expect(nextSignedMs(create(RecordSchema, { signedUnixMs: 5000n }), 1000)).toBe(5001n);
  });

  it("pass the fields to Rust, with an empty body when deleted", async () => {
    invoke.mockResolvedValueOnce({ signerKey: [1, 2], signature: [3, 4], signedUnixMs: 42 });
    const r = await signedRecord({ kind: "grant", id: "g1", body: new Uint8Array([9]), deleted: true }, { phrase: "twelve words" });
    expect(invoke).toHaveBeenCalledWith("sign_record", expect.objectContaining({ kind: "grant", id: "g1", body: [], deleted: true, phrase: "twelve words" }));
    expect(r.body).toEqual(new Uint8Array());
    expect(r.deleted).toBe(true);
    expect(r.signerKey).toEqual(new Uint8Array([1, 2]));
    expect(r.signature).toEqual(new Uint8Array([3, 4]));
    expect(r.signedUnixMs).toBe(42n);
  });

  it("name a key by the SHA-256 of its raw bytes", async () => {
    const raw = Buffer.from("03a107bff3ce10be1d70dd18e74bc09967e4d6309ba50d5f1ddc8664125531b8", "hex");
    const der = Buffer.concat([Buffer.from("302a300506032b6570032100", "hex"), raw]);
    const pem = `-----BEGIN PUBLIC KEY-----\n${der.toString("base64")}\n-----END PUBLIC KEY-----\n`;
    expect(await fingerprintOfPem(pem)).toBe(createHash("sha256").update(raw).digest("hex"));
  });
});

describe("grants (decision 189)", () => {
  beforeEach(() => {
    invoke.mockReset();
  });

  it("explain each protected option", () => {
    expect(optionNote("AddCapability=NET_ADMIN")).toMatch(/Linux capability NET_ADMIN/);
    expect(optionNote("AddDevice=/dev/dri")).toMatch(/hardware/);
    expect(optionNote("Network=host")).toMatch(/Node's own network/);
    expect(optionNote("Volume=/etc:/host-etc")).toMatch(/\/etc on the Node, outside its Storage Spaces/);
    expect(optionNote("PodmanArgs=--privileged")).toMatch(/Raw Podman arguments/);
    expect(optionNote("SecurityLabelDisable=true")).toMatch(/SELinux/);
    expect(optionNote("SeccompProfile=unconfined")).toMatch(/seccomp/);
    expect(optionNote("Sysctl=net.ipv4.ip_forward=1")).toMatch(/kernel setting \(net\.ipv4\.ip_forward=1\)/);
    expect(optionNote("[Service] ExecStartPre=/bin/sh -c id")).toMatch(/as root on the Node itself/);
    expect(optionNote("Something=else")).toMatch(/Lifts a limit/);
  });

  const need = create(GrantsNeededSchema, { nodeId: T.fingerprint, service: "vpn", imageRepository: "docker.io/acme/vpn", options: ["AddCapability=NET_ADMIN", "AddDevice=/dev/net/tun"], grantIds: ["g-cap", "g-tun"] });
  const refusal = () => new ConnectError("grants needed", Code.FailedPrecondition, undefined, [{ desc: GrantsNeededSchema, value: need }]);

  it("ask, sign one grant per option on the Service's Node, and retry once", async () => {
    const puts: ReturnType<typeof fromBinary<typeof PutRecordRequestSchema>>[] = [];
    invoke.mockImplementation(async (cmd: string, a: { procedure?: string; body?: number[]; signedUnixMs?: number; addr?: string }) => {
      if (cmd === "device_info") return { fingerprint: "c3", public_key_pem: "", public_key_path: "", name: "Laptop" };
      if (cmd === "sign_record") return { signerKey: [7], signature: [8], signedUnixMs: a.signedUnixMs };
      if (cmd !== "rpc") throw new Error(cmd);
      expect(a.addr).toBe(T.addr);
      if (a.procedure === "/maitre.v1.NodeService/ListRecords") return { status: 200, content_type: "application/proto", body: Array.from(toBinary(ListRecordsResponseSchema, create(ListRecordsResponseSchema, { records: [{ kind: "grant", id: "g-cap", deleted: true, signedUnixMs: 9_000_000_000_000n }] }))) };
      if (a.procedure === "/maitre.v1.NodeService/PutRecord") {
        puts.push(fromBinary(PutRecordRequestSchema, new Uint8Array(a.body!)));
        return { status: 200, content_type: "application/proto", body: Array.from(toBinary(PutRecordResponseSchema, create(PutRecordResponseSchema, {}))) };
      }
      throw new Error(a.procedure);
    });
    const ask = vi.fn(async () => true);
    setGrantAsker(ask);
    let calls = 0;
    const out = await withGrants(T, async () => {
      if (calls++ === 0) throw refusal();
      return "ran";
    });
    expect(out).toBe("ran");
    expect(calls).toBe(2);
    expect(ask).toHaveBeenCalledWith(expect.objectContaining({ service: "vpn" }));
    expect(puts.map((p) => p.record?.id)).toEqual(["g-cap", "g-tun"]);
    const g = fromBinary(GrantSchema, puts[0].record!.body);
    expect([g.nodeId, g.service, g.option, g.imageRepository, g.deviceName]).toEqual([T.fingerprint, "vpn", "AddCapability=NET_ADMIN", "docker.io/acme/vpn", "Laptop"]);
    // Replaces a revoked grant: signed after its tombstone.
    expect(puts[0].record!.signedUnixMs).toBe(9_000_000_000_001n);
    expect(puts[0].record!.signature).toEqual(new Uint8Array([8]));
  });

  it("stop quietly when declined, and retry only once", async () => {
    setGrantAsker(async () => false);
    await expect(withGrants(T, async () => Promise.reject(refusal()))).rejects.toBeInstanceOf(GrantsDeclined);
    expect(invoke).not.toHaveBeenCalled();

    invoke.mockImplementation(async (cmd: string, a: { procedure?: string; signedUnixMs?: number }) => {
      if (cmd === "device_info") return { name: "Laptop" };
      if (cmd === "sign_record") return { signerKey: [7], signature: [8], signedUnixMs: a.signedUnixMs };
      const body = a.procedure?.endsWith("ListRecords") ? toBinary(ListRecordsResponseSchema, create(ListRecordsResponseSchema, {})) : toBinary(PutRecordResponseSchema, create(PutRecordResponseSchema, {}));
      return { status: 200, content_type: "application/proto", body: Array.from(body) };
    });
    setGrantAsker(async () => true);
    let calls = 0;
    await expect(
      withGrants(T, async () => {
        calls++;
        throw refusal();
      }),
    ).rejects.toThrow(/grants needed/);
    expect(calls).toBe(2);
    setGrantAsker(undefined);
  });

  it("for a Catalog entry, sign only what is still needed and install without the features left out (G8)", async () => {
    const puts: string[] = [];
    invoke.mockImplementation(async (cmd: string, a: { procedure?: string; body?: number[]; signedUnixMs?: number }) => {
      if (cmd === "device_info") return { name: "Laptop" };
      if (cmd === "sign_record") return { signerKey: [7], signature: [8], signedUnixMs: a.signedUnixMs };
      if (a.procedure?.endsWith("PutRecord")) puts.push(fromBinary(PutRecordRequestSchema, new Uint8Array(a.body!)).record!.id);
      const body = a.procedure?.endsWith("ListRecords") ? toBinary(ListRecordsResponseSchema, create(ListRecordsResponseSchema, {})) : toBinary(PutRecordResponseSchema, create(PutRecordResponseSchema, {}));
      return { status: 200, content_type: "application/proto", body: Array.from(body) };
    });
    const entry = {
      id: "vpn",
      grants: [
        { option: "AddCapability=NET_ADMIN", why: "Routes traffic." },
        { option: "AddDevice=*", why: "A tunnel.", optional: "tunnel", without: "No tunnel." },
      ],
    } as unknown as CatalogEntry;
    const ask = vi.fn(async () => ({ without: ["tunnel"] }));
    setGrantAsker(ask);
    const without = vi.fn(async (fs: string[]) => `without ${fs.join()}`);
    const out = await withGrants(T, () => Promise.reject(refusal()), { entry, without });
    expect(ask).toHaveBeenCalledWith(expect.objectContaining({ service: "vpn" }), entry);
    expect(puts).toEqual(["g-cap"]);
    expect(without).toHaveBeenCalledWith(["tunnel"]);
    expect(out).toBe("without tunnel");
    // An option the entry did not declare gets the ordinary question.
    const plain = vi.fn(async () => true);
    setGrantAsker(plain);
    await withGrants(T, async () => (plain.mock.calls.length ? "ok" : Promise.reject(refusal())), { entry: { ...entry, grants: entry.grants!.slice(0, 1) }, without });
    expect(plain).toHaveBeenCalledWith(expect.objectContaining({ service: "vpn" }));
    setGrantAsker(undefined);
  });
});
