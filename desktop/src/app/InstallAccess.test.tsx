// @vitest-environment jsdom
// Decision 287: the install sheet offers all four kinds of access, with the
// host box and the public acknowledgement the Access editor has, and the
// installed Service carries the choice.
import "@/test/dom";
import { beforeAll, describe, expect, it } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

beforeAll(() => {
  localStorage.setItem("maitre.demo", "1");
  window.history.replaceState({}, "", "/?route=catalog");
});

describe("install sheet access (287)", () => {
  it("offers the four choices, needs a host and the acknowledgement for a public address, and installs with them", async () => {
    const { installTauriShim } = await import("@/demo/shim");
    installTauriShim();
    const { default: App } = await import("@/App");
    render(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <App />
      </QueryClientProvider>,
    );
    const main = await screen.findByRole("main", {}, { timeout: 5000 });
    const card = (await within(main).findByText("Memos", {}, { timeout: 5000 })).closest("button")!;
    fireEvent.click(card);
    const sheet = await screen.findByRole("dialog");
    await within(sheet).findByText("Install Memos");
    const group = within(sheet).getByRole("radiogroup", { name: "Who can reach memos" });
    const radios = within(group).getAllByRole("radio");
    expect(radios.map((r) => r.getAttribute("aria-label") ?? r.closest("label")?.textContent?.slice(0, 22))).toEqual(["At home onlyDevices on", "From anywhere, for you", "Public, at a Tailscale", "Public, at your own ad"]);
    // The demo's first Node is on Tailscale, so remote and public are open.
    await waitFor(() => expect(radios.every((r) => !(r as HTMLButtonElement).disabled)).toBe(true));
    const install = () => within(sheet).getByRole("button", { name: /^Install/ }) as HTMLButtonElement;
    expect(install().disabled).toBe(false);

    fireEvent.click(radios[3]);
    const host = within(sheet).getByLabelText("Host name") as HTMLInputElement;
    fireEvent.change(host, { target: { value: "notes.local" } });
    expect(within(sheet).getByText("That is not a public host name.")).toBeTruthy();
    expect(install().disabled).toBe(true);
    fireEvent.change(host, { target: { value: "Notes.Example.com" } });
    expect(install().disabled).toBe(true);
    fireEvent.click(within(sheet).getByRole("checkbox"));
    expect(install().disabled).toBe(false);
    fireEvent.click(install());
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull(), { timeout: 5000 });

    // Installed: the Service takes its place in the sidebar.
    await waitFor(() => expect(document.querySelector('[data-service="memos"]')).toBeTruthy(), { timeout: 5000 });
  }, 20000);
});
