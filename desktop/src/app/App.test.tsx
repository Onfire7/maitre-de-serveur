// @vitest-environment jsdom
// The whole Console against the demo Fleet: first answer through the anchor,
// sidebar filled from GetFleetStatus, tabs reachable.
import "@/test/dom";
import { describe, expect, it, beforeAll } from "vitest";
import { render, screen, within, fireEvent } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

beforeAll(() => {
  localStorage.setItem("maitre.demo", "1");
});

describe("Console on the demo Fleet", () => {
  it("shows every Service in the sidebar and the Fleet Overview with its notices", async () => {
    const { installTauriShim } = await import("@/demo/shim");
    installTauriShim();
    const { default: App } = await import("@/App");
    render(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <App />
      </QueryClientProvider>,
    );
    for (const name of ["adguard", "jellyfin", "minecraft", "paperless", "uptime"]) expect(await screen.findByText(name, {}, { timeout: 5000 })).toBeTruthy();
    expect(await screen.findByText(/connected through Alder/, {}, { timeout: 5000 })).toBeTruthy();
    expect(await screen.findByText(/Kitchen tablet is asking to join/)).toBeTruthy();
    // The Nodes tab shows both Nodes with their gauges.
    fireEvent.mouseDown(screen.getByRole("tab", { name: /Nodes/ }));
    fireEvent.click(screen.getByRole("tab", { name: /Nodes/ }));
    const main = screen.getByRole("main");
    expect(await within(main).findByText("Basalt", {}, { timeout: 5000 })).toBeTruthy();
    expect(within(main).getAllByText("CPU").length).toBe(2);
    // Only Basalt has a newer image; its Update opens the details' dialog.
    const update = await within(main).findByRole("button", { name: /^Update$/ }, { timeout: 5000 });
    expect(within(main).getAllByRole("button", { name: /^Update$/ }).length).toBe(1);
    fireEvent.click(update);
    expect(await screen.findByRole("dialog", { name: "Update Maître Server" }, { timeout: 5000 })).toBeTruthy();
    expect(screen.getByText("The registry has a newer build of this image.")).toBeTruthy();
  });
});
