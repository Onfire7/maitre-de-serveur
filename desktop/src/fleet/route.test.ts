// @vitest-environment jsdom
import { beforeEach, describe, expect, it } from "vitest";
import { chooseAddress, hostOf, isTailscale, rememberRoute, resetRoutes, routeKind, routeLabel, forgetRoute, pinned, setPinned } from "./route";

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
  it("prefers the way that answered last, until it is forgotten", () => {
    rememberRoute("a", "100.64.0.5:7443");
    expect(chooseAddress("a", addrs, "home")).toBe("100.64.0.5:7443");
    forgetRoute("a", "100.64.0.5:7443");
    expect(chooseAddress("a", addrs, "home")).toBe("192.168.4.74:7443");
  });
  it("keeps the pin on this device", () => {
    expect(pinned()).toBeUndefined();
    setPinned({ addr: "100.64.0.5:7443", fingerprint: "a" });
    expect(pinned()).toEqual({ addr: "100.64.0.5:7443", fingerprint: "a" });
    setPinned(undefined);
    expect(pinned()).toBeUndefined();
  });
});
