// @vitest-environment jsdom
// The Restart (Nodes list) and Roll back dialogs say how long the Node's
// Services stop: a restart is the same boot as an update's, so the update's
// downtime figure. On the demo Fleet Alder has one (95 s, about 2 minutes);
// Basalt has too few updated runs, so "a few minutes".
import "@/test/dom";
import { WHOLE_APP_TIMEOUT } from "@/test/app";
import { beforeAll, describe, expect, it } from "vitest";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

beforeAll(() => {
  localStorage.setItem("maitre.demo", "1");
});

function card(name: string): HTMLElement {
  return screen.getByRole("button", { name: `Rename ${name}` }).closest("div.rounded-xl") as HTMLElement;
}

describe("the Restart and Roll back dialogs", { timeout: WHOLE_APP_TIMEOUT }, () => {
  it("give the Node's typical downtime, or a few minutes without one", async () => {
    const { installTauriShim } = await import("@/demo/shim");
    installTauriShim();
    const { default: App } = await import("@/App");
    render(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <App />
      </QueryClientProvider>,
    );
    fireEvent.mouseDown(await screen.findByRole("tab", { name: /Nodes/ }, { timeout: 5000 }));
    fireEvent.click(screen.getByRole("tab", { name: /Nodes/ }));
    await screen.findByRole("button", { name: "Rename Alder" }, { timeout: 5000 });

    for (const [name, words] of [
      ["Alder", "about 2 minutes"],
      ["Basalt", "a few minutes"],
    ] as const) {
      // Restart, from the Node's card.
      fireEvent.click(within(card(name)).getByRole("button", { name: /Restart/ }));
      const restart = await screen.findByRole("alertdialog", { name: `Restart ${name}?` }, { timeout: 5000 });
      expect(await within(restart).findByText(new RegExp(`stop for ${words} while it comes back\\.`), {}, { timeout: 5000 })).toBeTruthy();
      expect(restart.textContent).not.toContain("a minute or two");
      fireEvent.click(within(restart).getByRole("button", { name: "Cancel" }));

      // Roll back, from the Node's details.
      fireEvent.click(within(card(name)).getByRole("button", { name: "Details" }));
      const roll = await within(card(name)).findByRole("button", { name: /Roll back…/ }, { timeout: 5000 });
      fireEvent.click(roll);
      const dialog = await screen.findByRole("alertdialog", { name: `Roll ${name} back?` }, { timeout: 5000 });
      expect(within(dialog).getByText(new RegExp(`Its Services stop for ${words}\\.`))).toBeTruthy();
      expect(dialog.textContent).not.toContain("a minute or two");
      fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
    }
  });
});
