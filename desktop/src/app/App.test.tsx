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
    fireEvent.keyDown(screen.getByRole("dialog"), { key: "Escape" });

    // Files: a new folder in Movies, then inside it only ".." (not "Empty.").
    fireEvent.mouseDown(screen.getByRole("tab", { name: /Files/ }));
    fireEvent.click(screen.getByRole("tab", { name: /Files/ }));
    fireEvent.click(await within(main).findByRole("button", { name: /Movies/ }, { timeout: 5000 }));
    expect(await within(main).findByRole("button", { name: ".." })).toBeTruthy();
    fireEvent.click(within(main).getByRole("button", { name: /New folder/ }));
    const input = await screen.findByLabelText("Name");
    fireEvent.change(input, { target: { value: "2026" } });
    fireEvent.keyDown(input, { key: "Enter" });
    fireEvent.click(await within(main).findByRole("button", { name: /^2026$/ }, { timeout: 5000 }));
    expect(await within(main).findByRole("button", { name: ".." })).toBeTruthy();
    expect(within(main).queryByText("Empty.")).toBeNull();
    fireEvent.click(within(main).getByRole("button", { name: ".." }));
    expect(await within(main).findByRole("button", { name: /Dune/ })).toBeTruthy();

    // Move Dune into Movies/2026: the dialog starts where it is, so Move is
    // refused until another folder is picked.
    fireEvent.click(within(main).getByRole("checkbox", { name: /Select Dune/ }));
    fireEvent.click(within(main).getByRole("button", { name: /Move 1 item/ }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText(/already here/)).toBeTruthy();
    expect((within(dialog).getByRole("button", { name: "Move here" }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(await within(dialog).findByRole("button", { name: "2026" }, { timeout: 5000 }));
    expect(within(dialog).getByText("Media / Movies / 2026")).toBeTruthy();
    fireEvent.click(within(dialog).getByRole("button", { name: "Move here" }));
    expect(await screen.findByText(/1 item queued to move/)).toBeTruthy();
  });
});
