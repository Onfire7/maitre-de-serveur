// @vitest-environment jsdom
import "@/test/dom";
import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { MotionConfig } from "motion/react";
import { create } from "@bufbuild/protobuf";
import { ServiceSpecSchema } from "@/gen/maitre/v1/service_pb";
import { TooltipProvider } from "@/components/ui/tooltip";
import type { FleetService } from "@/fleet/useFleet";
import { RouteProvider } from "./routes";
import { Sidebar, type SidebarNode } from "./Sidebar";

const svc = (name: string, word: FleetService["word"] = "Online"): FleetService => ({ spec: create(ServiceSpecSchema, { name, catalogId: "jellyfin", nodeId: "a" }), word, nodeUnreachable: false });
const services = [svc("adguard"), svc("jellyfin"), svc("minecraft", "Attention")];
const nodes: SidebarNode[] = [
  { nodeId: "a", name: "Alder", word: "Healthy" },
  { nodeId: "b", name: "Basalt", word: "Updating" },
];

function renderSidebar(collapsed = false, route: "service" | "fleet" = "fleet") {
  return render(
    <MotionConfig skipAnimations>
      <TooltipProvider>
        <RouteProvider initial={route === "fleet" ? { name: "fleet", tab: "overview" } : { name: "service", service: "jellyfin" }}>
          <Sidebar services={services} snap={undefined} nodes={nodes} fleetName="Home" badge={2} collapsed={collapsed} onToggle={() => {}} canAddService onFlyoutItem={() => {}} onPreferences={() => {}} />
        </RouteProvider>
      </TooltipProvider>
    </MotionConfig>,
  );
}

const row = (name: string) => document.querySelector<HTMLElement>(`[data-service="${name}"]`)!;
const menu = () => screen.queryByRole("menu");
// An exit ends with a resolved promise, so time is advanced inside an async act.
const settle = async () => {
  for (let i = 0; i < 10; i++)
    await act(async () => {
      vi.advanceTimersByTime(100);
    });
};

describe("Sidebar", () => {
  // Fake timers drive the hover and leave delays; Motion's frames run on the
  // same fake clock, and animations are skipped, so advancing time finishes
  // an exit.
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it("lists every Service and the Fleet entry with its badge and Nodes", () => {
    renderSidebar();
    for (const s of services) expect(row(s.spec.name)).toBeTruthy();
    expect(screen.getByText("Home")).toBeTruthy();
    expect(screen.getByText("2")).toBeTruthy();
    expect(screen.getByText("Alder")).toBeTruthy();
    expect(screen.getByText("Updating")).toBeTruthy();
  });

  it("opens a Service's flyout after a full second of hovering, not before (142)", () => {
    renderSidebar();
    fireEvent.mouseEnter(row("jellyfin"));
    act(() => vi.advanceTimersByTime(999));
    expect(menu()).toBeNull();
    act(() => vi.advanceTimersByTime(1));
    expect(menu()?.getAttribute("aria-label")).toBe("jellyfin actions");
  });

  it("opens at once on the chevron, follows the pointer, and closes on Escape", async () => {
    renderSidebar();
    fireEvent.mouseEnter(screen.getByRole("button", { name: "adguard actions" }));
    expect(menu()?.getAttribute("aria-label")).toBe("adguard actions");
    fireEvent.mouseEnter(row("minecraft"));
    expect(menu()?.getAttribute("aria-label")).toBe("minecraft actions");
    act(() => {
      fireEvent.keyDown(window, { key: "Escape" });
    });
    await settle();
    expect(menu()).toBeNull();
  });

  it("closes 300 ms after the pointer leaves both the list and the flyout", async () => {
    renderSidebar();
    fireEvent.mouseEnter(screen.getByRole("button", { name: "jellyfin actions" }));
    fireEvent.mouseLeave(row("jellyfin").closest("div.overflow-y-auto")!);
    act(() => vi.advanceTimersByTime(299));
    expect(menu()).not.toBeNull();
    act(() => vi.advanceTimersByTime(400));
    await settle();
    expect(menu()).toBeNull();
  });

  it("opens a Node's status flyout from its row, closing the Service one", async () => {
    renderSidebar();
    fireEvent.mouseEnter(screen.getByRole("button", { name: "jellyfin actions" }));
    expect(menu()).not.toBeNull();
    fireEvent.mouseEnter(screen.getByRole("button", { name: "Alder status" }));
    await settle();
    expect(menu()).toBeNull();
    // With no snapshot the Node flyout has nothing to show and renders nothing;
    // the Service flyout having closed is what this checks.
  });

  it("collapsed: the name tooltip opens at once and not while a flyout is open (142)", () => {
    renderSidebar(true);
    fireEvent.pointerMove(row("jellyfin"));
    fireEvent.focus(row("jellyfin"));
    act(() => vi.advanceTimersByTime(0));
    expect(screen.queryAllByText(/jellyfin · Online/).length).toBeGreaterThan(0);
    fireEvent.keyDown(row("jellyfin").querySelector("button")!, { key: "ArrowRight" });
    expect(menu()).not.toBeNull();
    expect(screen.queryAllByText(/jellyfin · Online/).length).toBe(0);
  });
});
