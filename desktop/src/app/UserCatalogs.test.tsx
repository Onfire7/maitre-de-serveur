// @vitest-environment jsdom
// User Catalogs on the demo Fleet (plan C9): their entries beside the
// project's, labelled; what changed since this Console last looked; and a
// Compose file added to a folder Catalog, its report shown at once and
// checked by a Node.
import "@/test/dom";
import { beforeAll, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

vi.mock("@tauri-apps/plugin-dialog", () => ({ open: vi.fn(async () => "/home/sean/Downloads/uptime-kuma/compose.yaml") }));

beforeAll(() => {
  localStorage.setItem("maitre.demo", "1");
  window.history.replaceState({}, "", "/?route=catalog");
  // This Console last looked at Community extras before linkding changed,
  // before IT Tools arrived, and while an old entry was still there.
  localStorage.setItem(
    "maitre.catalogSeen.community-extras",
    JSON.stringify({ linkding: JSON.stringify({ id: "linkding", summary: "An older summary" }, null, 2), "old-app": "{}" }),
  );
});

describe("user Catalogs", () => {
  it("shows their entries, what changed, and adds a Compose file with its report", async () => {
    const { installTauriShim } = await import("@/demo/shim");
    installTauriShim();
    const { default: App } = await import("@/App");
    render(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <App />
      </QueryClientProvider>,
    );
    const main = await screen.findByRole("main", {}, { timeout: 5000 });

    // Entries of both user Catalogs, labelled by Catalog; one can be shown alone.
    // (Waiting on whoami, which only a user Catalog has: the project's has an IT Tools too.)
    expect(await within(main).findByText("whoami", {}, { timeout: 5000 })).toBeTruthy();
    expect(within(main).getAllByText("IT Tools").length).toBe(2);
    expect(within(main).getAllByText("Community extras").length).toBeGreaterThan(0);
    fireEvent.click(within(within(main).getByRole("group", { name: "Catalog" })).getByRole("button", { name: "My apps" }));
    expect(within(main).queryByText("IT Tools")).toBeNull();
    expect(within(main).queryByText("Jellyfin")).toBeNull();
    fireEvent.click(within(within(main).getByRole("group", { name: "Catalog" })).getByRole("button", { name: "Every Catalog" }));

    // What changed since the last look, reviewed and marked as seen.
    expect(within(main).getByText(/changed since you last looked: 1 new, 1 changed, 1 removed/)).toBeTruthy();
    fireEvent.click(within(main).getByRole("button", { name: "Review" }));
    const changes = await screen.findByRole("dialog", { name: "What changed in Community extras" });
    expect(within(changes).getByText(/IT Tools: Handy converters/)).toBeTruthy();
    expect(within(changes).getByLabelText("Changes to linkding").textContent).toMatch(/-\s+"summary": "An older summary"/);
    expect(within(changes).getByText("old-app")).toBeTruthy();
    fireEvent.click(within(changes).getByRole("button", { name: "Mark as seen" }));
    expect(within(main).queryByText(/changed since you last looked/)).toBeNull();

    // The Catalogs sheet: the git Catalog with its unreadable entry, the folder with Add a Compose file.
    fireEvent.click(within(main).getByRole("button", { name: /Catalogs/ }));
    const sheet = await screen.findByRole("dialog", { name: "Catalogs" });
    expect(within(sheet).getByText(/1 that could not be read: homepage \(image must be pinned/)).toBeTruthy();
    expect(within(sheet).getByText(/at 4f2c9e1b/)).toBeTruthy();
    fireEvent.click(within(sheet).getByRole("button", { name: /Add a Compose file/ }));
    const report = await screen.findByRole("dialog", { name: "Uptime Kuma is in My apps" }, { timeout: 5000 });
    expect(within(report).getByText(/Written, but unfinished/)).toBeTruthy();
    expect(within(report).getByText("Needs a person")).toBeTruthy();
    expect(within(report).getByText(/Podman's Quadlet on the Node accepts every unit/)).toBeTruthy();
  }, 30_000);
});
