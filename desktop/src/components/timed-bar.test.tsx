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

  describe("an update's three sections", () => {
    // Usually 6 min in all, 2 of them down: 4 min of Downloading, 2 of Restarting, on an 8 min scale.
    const sections = (progress: number, restarted: number | null) => [
      { label: "Downloading", typicalMs: 4 * min, progress },
      { label: "Restarting", typicalMs: 2 * min, since: restarted },
    ];
    const section = (label: string) => screen.getByRole("progressbar").querySelector<HTMLElement>(`[title="${label}"]`)!;
    const fill = (label: string) => (section(label).firstElementChild as HTMLElement).style.width;

    it("sizes Downloading and Restarting from the figures, the typical end marked at 75%, and labels the sections under the bar", () => {
      render(<TimedBar since={t0} sections={sections(0, null)} />);
      expect(screen.getByText("usually about 6 min").closest("[data-marker]")!.getAttribute("style")).toContain("left: 75%");
      expect(section("Downloading").style.width).toBe("50%");
      expect(section("Restarting").style.width).toBe("25%");
      expect(section("Overtime").style.width).toBe("25%");
      const legend = screen.getByRole("list", { name: "Sections" });
      expect([...legend.children].map((c) => c.textContent)).toEqual(["Downloading", "Restarting", "Overtime"]);
      expect((legend.children[0] as HTMLElement).style.width).toBe("50%");
    });

    it("fills Downloading by the staging percentage, never past its width however slow", () => {
      const { rerender } = render(<TimedBar since={t0} sections={sections(0.5, null)} />);
      expect(fill("Downloading")).toBe("50%");
      expect(bar().getAttribute("aria-valuenow")).toBe("25");
      pass(20 * min);
      rerender(<TimedBar since={t0} sections={sections(1.4, null)} />);
      expect(fill("Downloading")).toBe("100%");
      expect(fill("Restarting")).toBe("0%");
      expect(fill("Overtime")).toBe("0%");
      expect(bar().dataset.state).toBe("running");
      expect(screen.getByText("20 min 0 s")).toBeTruthy();
    });

    it("fills Restarting by the time since the restart began, then turns red into Overtime past its figure plus 10%", () => {
      pass(3 * min);
      const restarted = Date.now();
      render(<TimedBar since={t0} sections={sections(1, restarted)} />);
      pass(min);
      expect(fill("Restarting")).toBe("50%");
      expect(screen.getByText("4 min 0 s")).toBeTruthy();
      pass(72_000); // 2 min 12 s into the restart: exactly its figure plus 10%
      expect(bar().dataset.state).toBe("running");
      expect(screen.queryByText(hint)).toBeNull();
      pass(1000);
      expect(bar().dataset.state).toBe("late");
      expect(screen.getByText(hint)).toBeTruthy();
      expect(fill("Restarting")).toBe("100%");
      expect(parseFloat(fill("Overtime"))).toBeGreaterThan(0);
      pass(10 * min);
      expect(bar().dataset.state).toBe("late");
      expect(fill("Overtime")).toBe("100%");
      expect(bar().getAttribute("aria-valuenow")).toBe("100");
    });
  });
});

