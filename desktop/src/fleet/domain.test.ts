// @vitest-environment jsdom
import { beforeEach, describe, expect, it } from "vitest";
import { create, toBinary } from "@bufbuild/protobuf";
import { EntryNodeSchema, FleetDomainSchema, RecordSchema } from "@/gen/maitre/v1/fleet_pb";
import type { FleetSnapshot } from "./useFleet";
import { PublicSettingsSchema } from "@/gen/maitre/v1/service_pb";
import { acmeEmail, domainAddr, domainOrder, fleetDomainName, holderOf, rememberDomain, validDomain } from "./domain";

function snapWith(app: boolean, holder?: string): FleetSnapshot {
  const body = toBinary(FleetDomainSchema, create(FleetDomainSchema, { domain: "home.example.com", app: create(EntryNodeSchema, { enabled: app }) }));
  const status = new Map([
    ["a", { entries: [] }],
    ["b", { entries: holder === "b" ? [{ role: "app", state: "forwarded 7443/tcp at eero" }] : [] }],
  ]);
  return { records: [create(RecordSchema, { kind: "fleet_domain", id: "fleet", body })], status } as unknown as FleetSnapshot;
}

beforeEach(() => localStorage.clear());

describe("the Fleet's domain", () => {
  it("is remembered for a start away from home only when the app's forward is on", () => {
    rememberDomain(snapWith(false));
    expect(domainAddr()).toBe("");
    expect(fleetDomainName()).toBe("home.example.com");
    rememberDomain(snapWith(true, "b"));
    expect(domainAddr()).toBe("home.example.com:7443");
  });

  it("tries the Node that held the app's forward first", () => {
    rememberDomain(snapWith(true, "b"));
    expect(domainOrder(["a", "b", "c"])).toEqual(["b", "a", "c"]);
    expect(holderOf(snapWith(true, "b"), "app")).toEqual({ nodeId: "b", state: "forwarded 7443/tcp at eero" });
    expect(holderOf(snapWith(true, "b"), "web")).toBeUndefined();
  });

  it("takes names on the internet only", () => {
    expect(validDomain("home.example.com")).toBe(true);
    expect(validDomain("nas.local")).toBe(false);
    expect(validDomain("Home.Example.com")).toBe(false);
    expect(validDomain("localhost")).toBe(false);
  });
});

describe("the certificate email", () => {
  it("is the Fleet's public_settings, ignoring a deleted one", () => {
    const rec = (deleted: boolean) => create(RecordSchema, { kind: "public_settings", id: "fleet", deleted, body: toBinary(PublicSettingsSchema, create(PublicSettingsSchema, { acmeEmail: "me@example.com" })) });
    expect(acmeEmail([rec(false)])).toBe("me@example.com");
    expect(acmeEmail([rec(true)])).toBe("");
    expect(acmeEmail(undefined)).toBe("");
  });
});
