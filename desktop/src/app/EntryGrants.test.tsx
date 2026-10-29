// @vitest-environment jsdom
// A Catalog entry that declares its grants (docs/popularity-and-grants.md,
// G8), on the demo Fleet: Home Assistant asks for the Node's own network
// with its reason, and installs without that feature when the person
// switches it off, with no grant signed.
import "@/test/dom";
import { beforeAll, describe, expect, it } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

beforeAll(() => {
  localStorage.setItem("maitre.demo", "1");
  window.history.replaceState({}, "", "/?route=catalog&install=home-assistant");
});

describe("declared grants", () => {
  it("asks with the entry's reasons and installs without an optional feature", async () => {
    const { installTauriShim } = await import("@/demo/shim");
    installTauriShim();
    const { demoFleet } = await import("@/demo/fleet");
    const { default: App } = await import("@/App");
    render(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <App />
      </QueryClientProvider>,
    );
    const sheet = await screen.findByRole("dialog", { name: /Install Home Assistant/ }, { timeout: 5000 });
    // Enabled once the Fleet's Spaces have loaded.
    const install = within(sheet).getByRole("button", { name: "Install" }) as HTMLButtonElement;
    await waitFor(() => expect(install.disabled).toBe(false), { timeout: 5000 });
    fireEvent.click(install);

    const ask = await screen.findByRole("alertdialog", { name: /Home Assistant asks for power over/ }, { timeout: 5000 });
    expect(within(ask).getByText(/Finds the devices on your home network/)).toBeTruthy();
    expect(within(ask).getByText(/open to your home network at the Node's address/)).toBeTruthy();
    expect(within(ask).getByText(/Without it: Add devices by their address/)).toBeTruthy();
    expect(within(ask).getByRole("button", { name: "Allow and install" })).toBeTruthy();
    fireEvent.click(within(ask).getByRole("switch", { name: "Allow discovery" }));
    fireEvent.click(within(ask).getByRole("button", { name: "Install without discovery" }));

    await screen.findByText(/home-assistant is installing/, {}, { timeout: 5000 });
    const f = demoFleet();
    const ha = f.services.find((s) => s.spec.name === "home-assistant");
    expect(ha?.spec.optionalGrants).toEqual({ discovery: false });
    expect(f.records.some((r) => r.kind === "grant" && !r.deleted && r.id.includes("home-assistant"))).toBe(false);
  }, 30_000);
});
