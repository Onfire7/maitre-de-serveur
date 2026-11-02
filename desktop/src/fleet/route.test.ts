// @vitest-environment jsdom
import { beforeEach, describe, expect, it } from "vitest";
import { chooseAddress, demoteRoute, demotedRoutes, hostOf, isTailscale, rememberRoute, resetRoutes, routeKind, routeLabel, routeOrder, pinned, setPinned } from "./route";

beforeEach(() => {
  localStorage.clear();
  resetRoutes();
});

describe("routeKind", () => {
  it("names each way to a Node", () => {
    expect(routeKind("192.168.4.74:7443")).toBe("home");
    expect(routeKind("10.0.2.15:7443")).toBe("home");
    expect(routeKind("172.20.1.4:7443")).toBe("home");
    expect(routeKind("alder.local:7443")).toBe("home");
    expect(routeKind("[fe80::1%eth0]:7443")).toBe("home");
    expect(routeKind("100.64.0.5:7443")).toBe("tailscale");
    expect(routeKind("100.127.255.1:7443")).toBe("tailscale");
    expect(routeKind("100.128.0.1:7443")).toBe("other");
    expect(routeKind("[fd7a:115c:a1e0::1]:7443")).toBe("tailscale");
    expect(routeKind("alder.home-fleet.ts.net:7443")).toBe("tailscale");
    expect(routeKind("home.example.com:7443", "home.example.com:7443")).toBe("domain");
    expect(routeKind("203.0.113.9:7443")).toBe("other");
  });
  it("has plain words", () => {
    expect(routeLabel("home")).toBe("at home");
    expect(routeLabel("tailscale")).toBe("over Tailscale");
  });
  it("splits the host", () => {
    expect(hostOf("[::1]:7443")).toBe("::1");
    expect(hostOf("alder.local")).toBe("alder.local");
    expect(isTailscale("alder.home-fleet.ts.net.")).toBe(true);
  });
});

describe("chooseAddress", () => {
  const addrs = ["192.168.4.74:7443", "100.64.0.5:7443"];
  it("takes the first when nothing is known", () => {
    expect(chooseAddress("a", addrs, undefined)).toBe("192.168.4.74:7443");
  });
  it("follows the anchor's kind, so Nodes reached over Tailscale take the others' Tailscale addresses", () => {
    expect(chooseAddress("a", addrs, "tailscale")).toBe("100.64.0.5:7443");
    expect(chooseAddress("a", ["192.168.4.75:7443"], "tailscale")).toBe("192.168.4.75:7443");
  });
  it("prefers the way that answered last, until it stops answering", () => {
    rememberRoute("a", "100.64.0.5:7443");
    expect(chooseAddress("a", addrs, "home")).toBe("100.64.0.5:7443");
    demoteRoute("a", "100.64.0.5:7443");
    expect(chooseAddress("a", addrs, "home")).toBe("192.168.4.74:7443");
  });
  const avalon = ["192.168.4.4:7443", "[fd28:fd81:ab35:1::c0d8]:7443", "100.74.111.122:7443"];
  it("takes Tailscale when the anchor was reached at the domain or by a public address: the device is away and the domain reaches one Node only", () => {
    expect(chooseAddress("a", avalon, "domain", "weyland.cc:7443")).toBe("100.74.111.122:7443");
    expect(chooseAddress("a", avalon, "other")).toBe("100.74.111.122:7443");
    expect(chooseAddress("a", avalon, "home")).toBe("192.168.4.4:7443");
    expect(routeOrder("a", [...avalon, "weyland.cc:7443"], "domain", "weyland.cc:7443")).toEqual(["100.74.111.122:7443", "192.168.4.4:7443", "[fd28:fd81:ab35:1::c0d8]:7443", "weyland.cc:7443"]);
  });
  it("puts a way that did not answer last, not out: it is only out of reach from here", () => {
    demoteRoute("a", "192.168.4.4:7443");
    expect(routeOrder("a", avalon, "home")).toEqual(["[fd28:fd81:ab35:1::c0d8]:7443", "100.74.111.122:7443", "192.168.4.4:7443"]);
    expect(chooseAddress("a", avalon, "home")).toBe("[fd28:fd81:ab35:1::c0d8]:7443");
    demoteRoute("a", "[fd28:fd81:ab35:1::c0d8]:7443");
    expect(routeOrder("a", avalon, undefined)).toEqual(["100.74.111.122:7443", "192.168.4.4:7443", "[fd28:fd81:ab35:1::c0d8]:7443"]);
    expect(demotedRoutes("a")).toEqual(["192.168.4.4:7443", "[fd28:fd81:ab35:1::c0d8]:7443"]);
    // Back home, the LAN address answers again and leads once more.
    rememberRoute("a", "192.168.4.4:7443");
    expect(demotedRoutes("a")).toEqual(["[fd28:fd81:ab35:1::c0d8]:7443"]);
    expect(routeOrder("a", avalon, "tailscale")).toEqual(["192.168.4.4:7443", "100.74.111.122:7443", "[fd28:fd81:ab35:1::c0d8]:7443"]);
    // Kept on this device.
    resetRoutes();
    expect(demotedRoutes("a")).toEqual(["[fd28:fd81:ab35:1::c0d8]:7443"]);
  });
  it("leaves a way out of the list alone", () => {
    rememberRoute("a", "203.0.113.9:7443");
    expect(routeOrder("a", addrs, undefined)).toEqual(addrs);
  });
  it("keeps the pin on this device", () => {
    expect(pinned()).toBeUndefined();
    setPinned({ addr: "100.64.0.5:7443", fingerprint: "a" });
    expect(pinned()).toEqual({ addr: "100.64.0.5:7443", fingerprint: "a" });
    setPinned(undefined);
    expect(pinned()).toBeUndefined();
  });
});
