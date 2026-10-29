// A Catalog entry of several containers becomes a Service spec the Node
// runs as such (plan C8), with its password made up at install (C7).
import { describe, expect, it } from "vitest";
import bundled from "@/catalog/index.json";
import { credits, formatStars, generatePassword, generatedSettings, grantMatches, sortEntries, specFor, type CatalogIndex } from "./services";

const entry = (id: string) => (bundled as CatalogIndex).entries.find((e) => e.id === id)!;

describe("specFor", () => {
  it("keeps a single-container entry as it was", () => {
    const spec = specFor(entry("jellyfin"), "", {});
    expect(spec.image).toBe(entry("jellyfin").image);
    expect(spec.components).toEqual([]);
    expect(spec.units).toEqual([]);
  });

  it("carries an entry's components, its bundle units and which one serves the page", () => {
    const spec = specFor(entry("immich"), "photos", {});
    expect(spec.image).toBe("");
    expect(spec.components.map((c) => c.name)).toEqual(["server", "machine-learning", "redis", "database"]);
    expect(spec.components[0].after).toEqual(["redis", "database"]);
    expect(spec.components[0].quadlet).toContain("{{ .Volumes.library }}");
    expect(spec.webComponent).toBe("server");
    expect(spec.units.map((u) => u.name)).toEqual(["model-cache.volume"]);
    expect(spec.networkInternal).toBe(false);
  });

  it("makes up a password for a setting that asks for one, never the word itself", () => {
    const pw = specFor(entry("immich"), "", {}).settings.DB_PASSWORD;
    expect(pw).toMatch(/^[A-Za-z0-9]{32}$/);
    expect(specFor(entry("immich"), "", {}).settings.DB_PASSWORD).not.toBe(pw);
    // What the install sheet shows is what is installed.
    const shown = generatedSettings(entry("immich"));
    expect(Object.keys(shown)).toEqual(["DB_PASSWORD"]);
    expect(specFor(entry("immich"), "", shown).settings.DB_PASSWORD).toBe(shown.DB_PASSWORD);
    expect(new Set(Array.from({ length: 50 }, generatePassword)).size).toBe(50);
  });
});

describe("credits", () => {
  it("names each project whose icons or app definitions the Catalog uses", () => {
    const c = credits(bundled as CatalogIndex);
    const icons = c.find((x) => x.name.startsWith("dashboard-icons"))!;
    expect(icons.licence).toBe("Apache License 2.0");
    expect(icons.what).toContain("Immich");
    expect(c.find((x) => x.name.startsWith("CasaOS"))?.what).toMatch(/App definitions of .*Vaultwarden/);
    expect(c.find((x) => x.name.startsWith("Big Bear"))?.what).toContain("Paperless-ngx");
  });

  it("bundles every icon, so the Console reaches no other server for them", () => {
    for (const e of (bundled as CatalogIndex).entries) expect(e.icon).toMatch(/^data:image\/(svg\+xml|png);base64,/);
  });
});

describe("popularity", () => {
  it("sorts by stars, entries without them after, by name", () => {
    const es = [{ name: "B", stars: 10 }, { name: "A" }, { name: "C", stars: 500 }, { name: "D", stars: 10 }, { name: "0" }];
    expect(sortEntries(es, "popular").map((e) => e.name)).toEqual(["C", "B", "D", "0", "A"]);
    expect(sortEntries(es, "name").map((e) => e.name)).toEqual(["0", "A", "B", "C", "D"]);
  });
  it("rounds stars as people say them", () => {
    expect([812, 8150, 81234, 117608].map(formatStars)).toEqual(["812", "8.1k", "81k", "117k"]);
  });
  it("the bundled Catalog has stars for every entry with a project", () => {
    // An app not developed in public (Plex) has no project, and no stars.
    const missing = (bundled as CatalogIndex).entries.filter((e) => e.project && !e.stars).map((e) => e.id);
    expect(missing).toEqual([]);
  });
});

describe("declared grants", () => {
  const e = {
    ...(bundled as CatalogIndex).entries[0],
    grants: [
      { option: "Network=host", why: "Finds devices.", optional: "discovery", without: "By address." },
      { option: "AddDevice=*", why: "A radio.", optional: "radio", without: "No Zigbee." },
      { option: "AddCapability=NET_RAW", why: "Pings." },
    ],
  };
  it("turns every feature on unless left out", () => {
    expect(specFor(e, "", {}).optionalGrants).toEqual({ discovery: true, radio: true });
    expect(specFor(e, "", {}, ["radio"]).optionalGrants).toEqual({ discovery: true, radio: false });
  });
  it("matches options as the Node words them", () => {
    expect(grantMatches(e.grants[1], "", "AddDevice=/dev/serial/by-id/usb-1")).toBe(true);
    expect(grantMatches(e.grants[0], "", "Network=host")).toBe(true);
    expect(grantMatches(e.grants[0], "web", "Network=host")).toBe(false);
    expect(grantMatches(e.grants[2], "", "AddCapability=NET_ADMIN")).toBe(false);
  });
});
