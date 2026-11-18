// @vitest-environment jsdom
// While a Node rolls back (or restarts), its details show the timed bar
// against its typical downtime, on the demo Fleet: Alder's is 95 s, so the
// marker says "usually about 2 min". The old guess is gone.
import "@/test/dom";
import { WHOLE_APP_TIMEOUT } from "@/test/app";
import { beforeAll, describe, expect, it } from "vitest";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

beforeAll(() => {
  localStorage.setItem("maitre.demo", "1");
});

describe("a Roll back's progress", { timeout: WHOLE_APP_TIMEOUT }, () => {
  it("is the timed bar with the Node's typical downtime marked", async () => {
    const { installTauriShim } = await import("@/demo/shim");
    installTauriShim();
    const { default: App } = await import("@/App");
    render(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <App />
      </QueryClientProvider>,
    );
    fireEvent.mouseDown(await screen.findByRole("tab", { name: /Updates/ }, { timeout: 20000 }));
    fireEvent.click(screen.getByRole("tab", { name: /Updates/ }));
    const main = screen.getByRole("main");
    const alder = await within(main).findByText("Updates usually take about 4 minutes; its Services are down for about 2.", {}, { timeout: 20000 });
    fireEvent.click(within(alder.closest("section")!).getByRole("button", { name: /Roll back…/ }));
    const dialog = await screen.findByRole("alertdialog", { name: /Roll Alder back/ }, { timeout: 20000 });
    fireEvent.keyDown(within(dialog).getByRole("button", { name: /Hold to/ }), { key: "Enter" });

    expect(await within(main).findByText("Rolling Alder back", {}, { timeout: 20000 })).toBeTruthy();
    expect(await within(main).findByText("usually about 2 min", {}, { timeout: 20000 })).toBeTruthy();
    expect(within(main).getByRole("progressbar").getAttribute("aria-valuetext")).toMatch(/^\d+ s$/);
    expect(within(main).queryByText(/normally takes under a minute/)).toBeNull();
  });
});
