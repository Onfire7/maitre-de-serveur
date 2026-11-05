import { describe, expect, it } from "vitest";
import { diffCount, lineDiff } from "./diff";

const marks = (before: string, after: string) => lineDiff(before, after).map((l) => `${l.mark === "added" ? "+" : l.mark === "removed" ? "-" : " "}${l.text}`);

describe("the Setup page's unit diff (ADR 0010)", () => {
  it("marks nothing when the unit is unchanged", () => {
    expect(marks("a\nb\n", "a\nb\n")).toEqual([" a", " b"]);
    expect(diffCount(lineDiff("a\nb\n", "a\nb\n"))).toEqual({ added: 0, removed: 0 });
  });

  it("keeps the lines that stay and marks what the change adds and removes", () => {
    expect(marks("[Container]\nImage=x\nNetwork=maitre.network\n", "[Container]\nImage=x\nNetwork=container:vpn\nNoNewPrivileges=true\n")).toEqual([
      " [Container]",
      " Image=x",
      "-Network=maitre.network",
      "+Network=container:vpn",
      "+NoNewPrivileges=true",
    ]);
    expect(diffCount(lineDiff("a\nb\nc\n", "a\nc\n"))).toEqual({ added: 0, removed: 1 });
  });

  it("a file that did not exist is no change to read line by line", () => {
    expect(marks("", "a\nb\n")).toEqual([" a", " b"]);
  });

  it("points each kept line at its place in the new text, so its source can be read", () => {
    const d = lineDiff("a\nx\nb\n", "a\nb\n");
    expect(d.map((l) => l.index)).toEqual([0, -1, 1]);
  });
});
