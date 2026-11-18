// @vitest-environment jsdom
// How long a Node's updates take, on the demo Fleet: Alder has three
// updated runs, so its own figures (about 4 minutes, down for about 2);
// Basalt has two, too few, so "a few minutes". Shown on each Node's card
// on the Updates page and in its details, and in the Update confirmation,
// which says the Services keep running when restarting is unticked.
import "@/test/dom";
import { WHOLE_APP_TIMEOUT } from "@/test/app";
import { beforeAll, describe, expect, it } from "vitest";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

beforeAll(() => {
  localStorage.setItem("maitre.demo", "1");
});

const alder = "Updates usually take about 4 minutes; its Services are down for about 2.";
const basalt = "Updates usually take a few minutes; its Services are down for a few minutes.";

describe("how long a Node's updates take", { timeout: WHOLE_APP_TIMEOUT }, () => {
  it("is on each Node's card, its details and the Update confirmation", async () => {
    const { installTauriShim } = await import("@/demo/shim");
    installTauriShim();
    const { default: App } = await import("@/App");
    render(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <App />
      </QueryClientProvider>,
    );
    fireEvent.mouseDown(await screen.findByRole("tab", { name: /Updates/ }, { timeout: 5000 }));
    fireEvent.click(screen.getByRole("tab", { name: /Updates/ }));
    const main = screen.getByRole("main");
    expect(await within(main).findByText(alder, {}, { timeout: 5000 })).toBeTruthy();
    expect(await within(main).findByText(basalt, {}, { timeout: 5000 })).toBeTruthy();

    // Alder's Update confirmation, then with restarting unticked.
    const card = within(main).getByText(alder).closest("section")!;
    fireEvent.click(within(card as HTMLElement).getByRole("button", { name: /Update…/ }));
    const dialog = await screen.findByRole("dialog", { name: "Update Maître Server" }, { timeout: 5000 });
    expect(within(dialog).getByText(alder)).toBeTruthy();
    fireEvent.click(within(dialog).getByRole("switch"));
    expect(within(dialog).getByText("Updates usually take about 4 minutes; its Services keep running until it next restarts.")).toBeTruthy();
    fireEvent.keyDown(dialog, { key: "Escape" });

    // The Node's details say the same.
    fireEvent.mouseDown(screen.getByRole("tab", { name: /Nodes/ }));
    fireEvent.click(screen.getByRole("tab", { name: /Nodes/ }));
    fireEvent.click(await within(main).findByText("Alder", {}, { timeout: 5000 }));
    expect(await within(main).findByText(alder, {}, { timeout: 5000 })).toBeTruthy();
  });
});
