import { describe, expect, it } from "vitest";
import { depotImage, type Depot } from "./depots";

const depot = (images: Depot["images"]): Depot => ({ registry: "192.168.4.54:8080", images, host: "b", addr: "192.168.4.54:8080" });

describe("depotImage", () => {
  it("offers the newest image", () => {
    const d = depot({ "maitre-server": [{ tag: "dev2", version: "3c29513", created: "2026-10-25T13:18:25Z" }, { tag: "dev", version: "9b57a3a", created: "2026-10-30T11:32:43Z" }] });
    expect(depotImage(d)).toEqual({ ref: "192.168.4.54:8080/maitre-server:dev", version: "9b57a3a", boot: "http://192.168.4.54:8080/boot/", insecure: "192.168.4.54:8080" });
  });
  it("has nothing to offer without images", () => {
    expect(depotImage(depot({}))).toBeNull();
  });
});
