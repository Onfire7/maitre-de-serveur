import { describe, expect, it } from "vitest";
import { create } from "@bufbuild/protobuf";
import { ServiceSpecSchema, ServiceStatusSchema } from "@/gen/maitre/v1/service_pb";
import { mainPage, pageByName, pageExposure, pageHost, pageViewOf, pageViews, pagesOf } from "./pages";
import type { FleetService } from "./useFleet";

const svc = (init: Parameters<typeof create<typeof ServiceSpecSchema>>[1], status?: Parameters<typeof create<typeof ServiceStatusSchema>>[1]): FleetService => ({
  spec: create(ServiceSpecSchema, init),
  status: status ? create(ServiceStatusSchema, status) : undefined,
  word: "Online",
  nodeUnreachable: false,
});

describe("Pages (ADR 0010)", () => {
  it("reads a spec from before Pages as one Page named web", () => {
    const spec = create(ServiceSpecSchema, { name: "jellyfin", webPort: 8096, webPath: "/web", webHostPort: 20001, healthHttp: "/health" });
    expect(pagesOf(spec)).toMatchObject([{ name: "web", port: 8096, path: "/web", hostPort: 20001, healthHttp: "/health" }]);
    expect(pageHost(spec, pagesOf(spec)[0])).toBe("jellyfin");
  });

  it("a Service without a page has none", () => {
    expect(pagesOf(create(ServiceSpecSchema, { name: "vpn" }))).toEqual([]);
    expect(mainPage(svc({ name: "vpn" }))).toBeUndefined();
  });

  it("names the first Page after the Service and the others after both", () => {
    const spec = create(ServiceSpecSchema, { name: "immich", pages: [{ name: "web", port: 2283 }, { name: "admin", port: 2283, path: "/admin", label: "Admin" }] });
    expect(pagesOf(spec).map((p) => pageHost(spec, p))).toEqual(["immich", "immich-admin"]);
    expect(pageByName(spec, "")?.name).toBe("web");
    expect(pageByName(spec, "admin")?.label).toBe("Admin");
    expect(pageByName(spec, "nope")).toBeUndefined();
  });

  it("a Page is reachable at its own level, else the Service's", () => {
    const spec = create(ServiceSpecSchema, { name: "immich", exposure: "private", pages: [{ name: "web", port: 1 }, { name: "admin", port: 2, exposure: "remote" }] });
    expect(pageExposure(spec, spec.pages[0])).toEqual({ exposure: "private", publicHost: "" });
    expect(pageExposure(spec, spec.pages[1])).toEqual({ exposure: "remote", publicHost: "" });
  });

  it("takes each Page's address from the status, and the Service-wide one for a Node from before Pages", () => {
    const several = svc(
      { name: "immich", pages: [{ name: "web", port: 2283 }, { name: "admin", port: 2283, path: "/admin", label: "Admin" }] },
      { url: "http://immich.local/", pages: [{ name: "web", url: "http://immich.local/" }, { name: "admin", url: "http://immich-admin.local/admin" }] },
    );
    expect(pageViews(several).map((v) => [v.host, v.url, v.label, v.first])).toEqual([
      ["immich", "http://immich.local/", "web", true],
      ["immich-admin", "http://immich-admin.local/admin", "Admin", false],
    ]);
    const old = svc({ name: "jellyfin", webPort: 8096, webHostPort: 20001 }, { url: "http://jellyfin.local/", healthy: true });
    expect(pageViews(old)).toMatchObject([{ host: "jellyfin", url: "http://jellyfin.local/" }]);
    expect(pageViewOf(old, "web")?.url).toBe("http://jellyfin.local/");
    expect(pageViewOf(several, "admin")?.host).toBe("immich-admin");
  });
});
