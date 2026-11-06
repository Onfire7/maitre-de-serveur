import { describe, expect, it } from "vitest";
import { create } from "@bufbuild/protobuf";
import { ServiceSpecSchema, ServiceStatusSchema } from "@/gen/maitre/v1/service_pb";
import { domainLinkCheck, mainPage, pageByName, pageExposure, pageHost, pageLink, pageViewOf, pageViews, pagesOf } from "./pages";
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

describe("links at the domain (decision 329)", () => {
  const stash = (homeState: string, remoteState = "up") =>
    svc(
      { name: "stash", exposure: "direct", publicHost: "stash.weyland.cc", pages: [{ name: "web", port: 9999 }] },
      { url: "http://stash.local", pages: [{ name: "web", url: "http://stash.local", remoteUrl: "https://stash.weyland.cc", remoteState, homeState }] },
    );
  const link = (s: FleetService, prefer: boolean) => pageLink(s.spec, s.spec.pages[0], s.status!.pages[0], prefer);

  it("opens the domain address only when asked and it answers from home", () => {
    expect(link(stash("up"), true)).toBe("https://stash.weyland.cc");
    expect(link(stash("up"), false)).toBe("http://stash.local");
    expect(link(stash("not from home: the router does not send 203.0.113.7 back in"), true)).toBe("http://stash.local");
    expect(link(stash(""), true)).toBe("http://stash.local");
    expect(link(stash("up", "waiting for DNS"), true)).toBe("http://stash.local");
  });

  it("leaves a Page that is not at the domain at .local", () => {
    const s = svc({ name: "kuma", exposure: "remote", pages: [{ name: "web", port: 1 }] }, { pages: [{ name: "web", url: "http://kuma.local", remoteUrl: "https://kuma.tail.ts.net", remoteState: "up", homeState: "up" }] });
    expect(link(s, true)).toBe("http://kuma.local");
  });

  it("sorts the Pages at the domain by how they answer from home", () => {
    const kuma = svc({ name: "kuma", exposure: "private", pages: [{ name: "web", port: 1 }] }, { pages: [{ name: "web", url: "http://kuma.local" }] });
    const away = { ...stash("not from home: x"), spec: create(ServiceSpecSchema, { name: "immich", exposure: "direct", pages: [{ name: "web", port: 1 }] }) };
    expect(domainLinkCheck([stash("up"), away, stash(""), kuma])).toEqual({ home: ["stash"], away: [{ host: "immich", why: "not from home: x" }], unchecked: ["stash"] });
  });
});
