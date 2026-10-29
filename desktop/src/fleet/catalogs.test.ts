// @vitest-environment jsdom
import { beforeEach, describe, expect, it } from "vitest";
import { create, toBinary } from "@bufbuild/protobuf";
import { RecordSchema } from "@/gen/maitre/v1/fleet_pb";
import { UserCatalogSchema, UserCatalogsSchema } from "@/gen/maitre/v1/service_pb";
import { catalogKey, specFor, type CatalogEntry } from "@/services";
import { catalogId, changeCount, changesSince, diffLines, fleetCatalogs, loadCatalog, markSeen, type LoadedCatalog } from "./catalogs";

const entry = (id: string, summary = "s"): CatalogEntry => ({ id, name: id, summary, description: "", icon: "data:x", category: "tools", image: "i", version: "1", exposure: "private", own_login: false, infrastructure: false, volumes: [], quadlet: "[Container]\n", catalog: { id: "c", name: "C" } });
const loaded = (entries: CatalogEntry[]): LoadedCatalog => ({ catalog: create(UserCatalogSchema, { id: "c", name: "C", gitUrl: "https://example.invalid/c.git" }), entries, problems: [], commit: "abc", dir: "/x" });

beforeEach(() => localStorage.clear());

describe("user Catalogs", () => {
  it("reads the Fleet's list from its record", () => {
    const body = toBinary(UserCatalogsSchema, create(UserCatalogsSchema, { catalogs: [{ id: "extras", name: "Extras", gitUrl: "https://example.invalid/x.git" }] }));
    expect(fleetCatalogs([create(RecordSchema, { kind: "catalogs", id: "fleet", body })]).map((c) => c.id)).toEqual(["extras"]);
    expect(fleetCatalogs([create(RecordSchema, { kind: "catalogs", id: "fleet", body, deleted: true })])).toEqual([]);
    expect(fleetCatalogs(undefined)).toEqual([]);
  });

  it("makes ids of names, never the project's own", () => {
    expect(catalogId("My Apps!", [])).toBe("my-apps");
    expect(catalogId("My Apps", ["my-apps"])).toBe("my-apps-2");
    expect(catalogId("Maître", [])).not.toBe("maitre");
  });

  it("names a user Catalog's entry in the Service it installs", () => {
    expect(catalogKey(entry("app"))).toBe("c/app");
    expect(specFor(entry("app"), "", {}).catalogId).toBe("c/app");
    expect(specFor({ ...entry("app"), catalog: undefined }, "", {}).catalogId).toBe("app");
  });

  it("does not read a folder on another computer", async () => {
    const c = create(UserCatalogSchema, { id: "f", name: "F", folder: "/elsewhere", folderDevice: "other" });
    expect((await loadCatalog(c, "me")).elsewhere).toBe(true);
  });

  it("says what changed since it was last seen, with a diff, and nothing on first sight", () => {
    const before = loaded([entry("a"), entry("b", "old words"), entry("gone")]);
    expect(changeCount(changesSince(before))).toBe(0);
    markSeen(before);
    const after = loaded([entry("a"), { ...entry("b", "new words"), icon: "data:changed-icon-only" }, entry("new")]);
    const ch = changesSince(after);
    expect(ch.added.map((e) => e.id)).toEqual(["new"]);
    expect(ch.removed).toEqual(["gone"]);
    expect(ch.changed.map((c) => c.entry.id)).toEqual(["b"]);
    expect(ch.changed[0].diff.filter((l) => l.kind !== " ").map((l) => l.kind + l.text.trim())).toEqual(['-"summary": "old words",', '+"summary": "new words",']);
    markSeen(after);
    expect(changeCount(changesSince(after))).toBe(0);
  });

  it("diffs lines with a little context", () => {
    const d = diffLines("1\n2\n3\n4\n5\n6\n7\n8", "1\n2\n3\n4\nX\n6\n7\n8");
    expect(d.map((l) => l.kind + l.text)).toEqual([" 3", " 4", "-5", "+X", " 6", " 7"]);
  });
});
