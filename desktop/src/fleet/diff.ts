// A line diff for the Setup page's preview (ADR 0010): what applying a
// Setup would change in a Service's unit files. Plain Myers-style LCS over
// whole lines; unit files are tens of lines, so the quadratic table costs
// nothing and the result reads the way a person expects.

export type DiffMark = "same" | "added" | "removed";

export interface DiffLine {
  mark: DiffMark;
  text: string;
  /** The line's index in the new text, for its source; -1 when removed. */
  index: number;
}

/** The lines of `after`, with the lines of `before` that are gone marked
 * where they were. An empty `before` marks nothing: a new file is not a
 * change to read line by line. */
export function lineDiff(before: string, after: string): DiffLine[] {
  const a = split(before);
  const b = split(after);
  if (before === after) return b.map((text, index) => ({ mark: "same", text, index }));
  if (a.length === 0) return b.map((text, index) => ({ mark: "same", text, index }));
  // table[i][j] is the length of the longest common run of a[i:] and b[j:].
  const table: number[][] = Array.from({ length: a.length + 1 }, () => new Array<number>(b.length + 1).fill(0));
  for (let i = a.length - 1; i >= 0; i--) {
    for (let j = b.length - 1; j >= 0; j--) {
      table[i][j] = a[i] === b[j] ? table[i + 1][j + 1] + 1 : Math.max(table[i + 1][j], table[i][j + 1]);
    }
  }
  const out: DiffLine[] = [];
  let i = 0;
  let j = 0;
  while (i < a.length && j < b.length) {
    if (a[i] === b[j]) {
      out.push({ mark: "same", text: b[j], index: j });
      i++;
      j++;
    } else if (table[i + 1][j] >= table[i][j + 1]) {
      out.push({ mark: "removed", text: a[i], index: -1 });
      i++;
    } else {
      out.push({ mark: "added", text: b[j], index: j });
      j++;
    }
  }
  for (; i < a.length; i++) out.push({ mark: "removed", text: a[i], index: -1 });
  for (; j < b.length; j++) out.push({ mark: "added", text: b[j], index: j });
  return out;
}

/** How many lines the diff adds and removes. */
export function diffCount(lines: DiffLine[]): { added: number; removed: number } {
  return { added: lines.filter((l) => l.mark === "added").length, removed: lines.filter((l) => l.mark === "removed").length };
}

function split(text: string): string[] {
  const t = text.endsWith("\n") ? text.slice(0, -1) : text;
  return t === "" ? [] : t.split("\n");
}
