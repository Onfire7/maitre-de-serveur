// @vitest-environment jsdom
// An update's progress on its Node's card: one bar in Downloading,
// Restarting and Overtime, sized from the Node's figures kept on the job;
// Downloading only when it stages without a restart; without figures the
// staging percentage, then the pulsing bar. Driven by a fake clock.
import "@/test/dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen } from "@testing-library/react";
import type { Job } from "@/fleet/updateQueue";
import { UpdateProgress } from "./UpdateProgress";

const t0 = new Date("2026-11-17T10:00:00Z").getTime();
const min = 60_000;
const hint = "Taking longer than usual. Its monitor shows the Maître banner when it is back, or an error if it is not.";

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(t0);
});
afterEach(() => vi.useRealTimers());

const pass = (ms: number) => act(() => vi.advanceTimersByTime(ms));
const bar = () => screen.getByRole("progressbar");
const sections = () => [...screen.getByRole("list", { name: "Sections" }).children].map((c) => c.textContent);
const width = (label: string) => bar().querySelector<HTMLElement>(`[title="${label}"]`)!.style.width;
const fill = (label: string) => (bar().querySelector<HTMLElement>(`[title="${label}"]`)!.firstElementChild as HTMLElement).style.width;

function job(p: Partial<Job>): Job {
  return {
    id: 1,
    nodeId: "a",
    name: "Alder",
    m: { kind: "update", target: "img", reboot: true },
    phase: "working",
    progress: 0,
    message: "downloading",
    started: t0,
    restarted: null,
    typical: { updateMs: 6 * min, downtimeMs: 2 * min },
    ...p,
  };
}

describe("an update's progress", () => {
  it("is three sections sized from the Node's figures, the typical end marked at 75%", () => {
    render(<UpdateProgress job={job({ progress: 0.25 })} />);
    expect(sections()).toEqual(["Downloading", "Restarting", "Overtime"]);
    expect(screen.getByText("usually about 6 min")).toBeTruthy();
    expect(width("Downloading")).toBe("50%");
    expect(width("Restarting")).toBe("25%");
    expect(fill("Downloading")).toBe("25%");
    expect(screen.getByText("downloading")).toBeTruthy();
  });

  it("fills Restarting by the time since the restart began, and turns red with the hint past its figure plus 10%", () => {
    pass(4 * min);
    const restarted = Date.now();
    const { rerender } = render(<UpdateProgress job={job({ phase: "down", progress: 0.9, restarted })} />);
    // Staging is over once the Node restarts.
    expect(fill("Downloading")).toBe("100%");
    pass(min);
    rerender(<UpdateProgress job={job({ phase: "down", progress: 0.9, restarted })} />);
    expect(fill("Restarting")).toBe("50%");
    expect(screen.getByText("5 min 0 s")).toBeTruthy();
    pass(73_000);
    expect(bar().dataset.state).toBe("late");
    expect(screen.getByText(hint)).toBeTruthy();
    pass(10 * min);
    expect(bar().getAttribute("aria-valuenow")).toBe("100");
  });

  it("shows only Downloading when it stages without a restart", () => {
    render(<UpdateProgress job={job({ m: { kind: "update", target: "img", reboot: false }, progress: 0.5 })} />);
    expect(sections()).toEqual(["Downloading", "Overtime"]);
    // Staging usually takes 4 min of the 6.
    expect(screen.getByText("usually about 4 min")).toBeTruthy();
    expect(fill("Downloading")).toBe("50%");
  });

  it("without figures shows the staging percentage, then the pulsing bar with the elapsed time", () => {
    const none = { updateMs: 0, downtimeMs: 0 };
    const { rerender } = render(<UpdateProgress job={job({ typical: none, progress: 0.4 })} />);
    expect(bar().getAttribute("aria-valuenow")).toBe("40");
    expect(screen.queryByText(/usually/)).toBeNull();
    pass(3 * min);
    rerender(<UpdateProgress job={job({ typical: none, phase: "down", progress: 1, restarted: Date.now() })} />);
    expect(bar().getAttribute("aria-valuenow")).toBeNull();
    expect(bar().getAttribute("aria-valuetext")).toBe("3 min 0 s");
    expect(screen.queryByText(/usually/)).toBeNull();
  });

  it("without a downtime figure falls back too, rather than a Restarting section of no width", () => {
    render(<UpdateProgress job={job({ typical: { updateMs: 6 * min, downtimeMs: 0 }, phase: "down", restarted: t0 })} />);
    expect(screen.queryByText(/usually/)).toBeNull();
    expect(screen.queryByText(hint)).toBeNull();
  });
});
