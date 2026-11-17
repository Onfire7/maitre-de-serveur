// @vitest-environment jsdom
// The timed bar a Restart or Roll back shows, against the Node's typical
// downtime: the figure at 75% of the scale, red past it plus a tenth, full
// past the end; without a figure the pulsing bar. Driven by a fake clock.
import "@/test/dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen } from "@testing-library/react";
import { TimedBar } from "./timed-bar";

const t0 = new Date("2026-11-17T10:00:00Z").getTime();
const min = 60_000;
const hint = "Taking longer than usual. Its monitor shows the Maître banner when it is back, or an error if it is not.";

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(t0);
});
afterEach(() => vi.useRealTimers());

const bar = () => screen.getByRole("progressbar");
const pass = (ms: number) => act(() => vi.advanceTimersByTime(ms));

describe("TimedBar", () => {
  it("marks the typical downtime at 75%, labelled, and shows the time elapsed", () => {
    render(<TimedBar since={t0} sections={[{ label: "Restarting", typicalMs: 2 * min, since: t0 }]} />);
    const marker = screen.getByText("usually about 2 min");
    expect(marker.closest("[data-marker]")!.getAttribute("style")).toContain("left: 75%");
    expect(screen.getByText("0 s")).toBeTruthy();
    pass(30_000);
    expect(screen.getByText("30 s")).toBeTruthy();
    // 30 s of a 160 s scale.
    expect(bar().getAttribute("aria-valuenow")).toBe("19");
    pass(60_000);
    expect(screen.getByText("1 min 30 s")).toBeTruthy();
  });

  it("turns red with the hint just past the typical downtime plus 10%, and stays full past the end", () => {
    render(<TimedBar since={t0} sections={[{ label: "Restarting", typicalMs: 2 * min, since: t0 }]} />);
    pass(132_000); // exactly 2 min plus 10%
    expect(bar().dataset.state).toBe("running");
    expect(screen.queryByText(hint)).toBeNull();
    pass(1000);
    expect(bar().dataset.state).toBe("late");
    expect(screen.getByText(hint)).toBeTruthy();
    expect(bar().getAttribute("aria-valuenow")).toBe("83");
    pass(5 * min);
    expect(bar().dataset.state).toBe("late");
    expect(bar().getAttribute("aria-valuenow")).toBe("100");
    expect(screen.getByText("7 min 13 s")).toBeTruthy();
  });

  it("without a figure, pulses with the elapsed time and no marker", () => {
    render(<TimedBar since={t0} sections={[{ label: "Restarting", typicalMs: 0, since: t0 }]} />);
    pass(5 * min);
    expect(screen.getByText("5 min 0 s")).toBeTruthy();
    expect(document.querySelector("[data-marker]")).toBeNull();
    expect(bar().getAttribute("aria-valuenow")).toBeNull();
    expect(screen.queryByText(hint)).toBeNull();
  });
});
