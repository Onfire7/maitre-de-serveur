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
    // Each in the sidebar, and again among the Overview's Services (209).
    for (const name of ["adguard", "jellyfin", "minecraft", "paperless", "uptime"]) expect((await screen.findAllByText(name, {}, { timeout: 5000 })).length).toBeGreaterThan(0);
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
    // Basalt's details are open: Logs opens the Logs tab on its agent (187).
    fireEvent.click(await within(main).findByRole("button", { name: "Logs" }));
    const log = await within(main).findByRole("log", { name: "Basalt: Maître agent" });
    expect(await within(log).findAllByText(/maitre\[\d+\]: /, {}, { timeout: 5000 })).not.toHaveLength(0);
    // Search highlights matches and counts them; Only matching lines hides the rest.
    fireEvent.change(within(main).getByLabelText("Search the log"), { target: { value: "silent" } });
    expect(within(main).getByText(/^1\d* of \d+$|^\d+ of \d+$/)).toBeTruthy();
    expect(log.querySelectorAll("mark").length).toBeGreaterThan(0);
    const all = log.querySelectorAll("[data-line]").length;
    fireEvent.click(within(main).getByRole("switch"));
    expect(log.querySelectorAll("[data-line]").length).toBeLessThan(all);

    // Files: a new folder in Movies, then inside it only ".." (not "Empty.").
    fireEvent.mouseDown(screen.getByRole("tab", { name: /Files/ }));
    fireEvent.click(screen.getByRole("tab", { name: /Files/ }));
    // Files opens on the whole Fleet (198): its places, Media first.
    expect(await within(main).findByRole("columnheader", { name: "Holds" }, { timeout: 5000 })).toBeTruthy();
    fireEvent.click((await within(main).findAllByRole("button", { name: /^Media$/ }))[0]);
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
    fireEvent.click(within(main).getByRole("button", { name: "Move…" }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText(/already here/)).toBeTruthy();
    expect((within(dialog).getByRole("button", { name: "Move here" }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(await within(dialog).findByRole("button", { name: "2026" }, { timeout: 5000 }));
    expect(within(dialog).getByText("Media / Movies / 2026")).toBeTruthy();
    fireEvent.click(within(dialog).getByRole("button", { name: "Move here" }));
    expect(await screen.findByText(/1 item queued to move/)).toBeTruthy();

    // Documents: a file opens in the side panel, which steps to the next
    // file and goes full screen; Zip queues a job named after the file.
    fireEvent.click(within(main).getByRole("button", { name: ".." }));
    fireEvent.click(await within(main).findByRole("button", { name: "Documents" }, { timeout: 5000 }));
    fireEvent.click(await within(main).findByRole("button", { name: "README.md" }, { timeout: 5000 }));
    const panel = await screen.findByRole("complementary", { name: "Preview of README.md" });
    fireEvent.click(within(panel).getByRole("button", { name: "Next file" }));
    expect(await screen.findByRole("complementary", { name: /Preview of running-costs.xlsx/ })).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Full screen" }));
    expect(screen.getByRole("complementary", { name: /Preview of running-costs.xlsx/ }).className).toContain("fixed");
    fireEvent.click(screen.getByRole("button", { name: "Close preview" }));
    fireEvent.click(within(main).getByRole("checkbox", { name: "Select notes.txt" }));
    fireEvent.click(within(main).getByRole("button", { name: /Zip/ }));
    expect(await screen.findByText("Zipping into notes.txt.zip")).toBeTruthy();
    // Delete asks first, listing what goes, and needs a hold.
    fireEvent.click(within(main).getByRole("checkbox", { name: "Select lake.jpg" }));
    fireEvent.click(within(main).getByRole("button", { name: /Delete/ }));
    const del = await screen.findByRole("dialog", { name: "Delete lake.jpg?" });
    expect(within(del).getByText(/cannot be undone/)).toBeTruthy();
    expect(within(del).getByRole("button", { name: /Hold to delete/ })).toBeTruthy();
  });
});
