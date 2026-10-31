import { describe, expect, it } from "vitest";
import { depotImage, type Depot } from "./depots";

const depot = (images: Depot["images"]): Depot => ({ registry: "192.168.4.54:8080", images, host: "b", addr: "192.168.4.54:8080" });

describe("depotImage", () => {
  it("offers the newest image", () => {
    const d = depot({ "maitre-server": [{ tag: "dev2", version: "3c29513", created: "2026-10-25T13:18:25Z" }, { tag: "dev", version: "9b57a3a", created: "2026-10-30T11:32:43Z" }] });
    expect(depotImage(d)).toEqual({ ref: "192.168.4.54:8080/maitre-server:dev", version: "9b57a3a", created: "2026-10-30T11:32:43Z", boot: "http://192.168.4.54:8080/boot/", insecure: "192.168.4.54:8080" });
  });
  it("has nothing to offer without images", () => {
    expect(depotImage(depot({}))).toBeNull();
  });
});

describe("bootFilesBehind (decision 263)", () => {
  it("names the files the Depot rebuilt and ignores the rest", async () => {
    const { bootFilesBehind } = await import("./depots");
    const local = { vmlinuz: "a", "rootfs.squashfs": "b", "shimx64.efi": "c" };
    expect(bootFilesBehind(local, { vmlinuz: "a", "rootfs.squashfs": "B", "shimx64.efi": "c", "new.bin": "z" })).toEqual(["rootfs.squashfs"]);
    expect(bootFilesBehind(local, { vmlinuz: "a" })).toEqual([]);
    expect(bootFilesBehind(local, null)).toEqual([]);
    expect(bootFilesBehind(undefined, { vmlinuz: "x" })).toEqual([]);
  });
});

describe("buildWords (decision 267)", () => {
  it("says the version and time it has", async () => {
    const { buildWords } = await import("./depots");
    expect(buildWords("778bc2d", "2026-10-31T19:26:12Z")).toMatch(/^778bc2d, built .*Oct.*\d\d?:\d\d/);
    expect(buildWords("", "2026-10-31T19:26:12Z", "downloaded here")).toMatch(/^downloaded here .*Oct/);
    expect(buildWords("778bc2d", "")).toBe("778bc2d");
    expect(buildWords("", "not a date")).toBe("unknown version");
  });
});
