import { describe, expect, it } from "vitest";
import { notices } from "./notices";

const snapWith = (st: unknown) => ({ nodes: [{ nodeId: "a", name: "Avalon" }], status: new Map([["a", st]]), spaces: [{ defaultForNew: true }] }) as never;

describe("a staged update", () => {
  // Decision 327's evening: five staged-and-unapplied deployments sat on a
  // Node and one of them applied at an unrelated reboot. The notice existed
  // but said only "restart it to run the new version", which reads as
  // something you choose to do rather than something that will happen.
  it("names the version that will come up and says any restart will do", () => {
    const n = notices(snapWith({ nodeId: "a", reachable: true, updateStaged: true, stagedVersion: "5bfa370", problems: [] }), [], null);
    const staged = n.find((x) => x.id === "staged:a");
    expect(staged?.title).toBe("Avalon will start 5bfa370 at its next restart");
    expect(staged?.detail).toMatch(/power cut|failed boot/);
    // It is not a fault, so it does not shout.
    expect(staged?.severity).toBe("info");
  });

  it("still shows while something else is wrong with the Node", () => {
    // Each of these used to win the else-if chain and hide the staging, so
    // the Node you most wanted to know about was the one that said nothing.
    for (const other of [
      { nodeId: "a", reachable: false, updateStaged: true, stagedVersion: "v2", problems: [] },
      { nodeId: "a", reachable: true, updateState: 3, updateMessage: "pull failed", updateStaged: true, stagedVersion: "v2", problems: [] },
      { nodeId: "a", reachable: true, lastRollback: "greenboot rolled it back", updateStaged: true, stagedVersion: "v2", problems: [] },
      { nodeId: "a", reachable: true, updateStaged: true, stagedVersion: "v2", problems: ["is low on space"] },
    ]) {
      const ids = notices(snapWith(other), [], null).map((x) => x.id);
      expect(ids).toContain("staged:a");
      expect(ids.length).toBeGreaterThan(1);
    }
  });

  it("says nothing when nothing is staged", () => {
    const ids = notices(snapWith({ nodeId: "a", reachable: true, problems: [] }), [], null).map((x) => x.id);
    expect(ids).not.toContain("staged:a");
  });
});
