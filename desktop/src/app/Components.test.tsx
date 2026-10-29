// @vitest-environment jsdom
// A Service of several containers in the Console (plan C1), against the
// demo Fleet's Immich: its open row lists each container with its state,
// changes one container's image, and opens one container's logs.
import "@/test/dom";
import { beforeAll, describe, expect, it } from "vitest";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

beforeAll(() => {
  localStorage.setItem("maitre.demo", "1");
});

describe("a Service of several containers", () => {
  it("lists its containers, changes one's image and opens one's logs", async () => {
    const { installTauriShim } = await import("@/demo/shim");
    installTauriShim();
    const { default: App } = await import("@/App");
    render(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <App />
      </QueryClientProvider>,
    );
    fireEvent.mouseDown(await screen.findByRole("tab", { name: /Services/ }, { timeout: 5000 }));
    fireEvent.click(screen.getByRole("tab", { name: /Services/ }));
    const main = screen.getByRole("main");
    fireEvent.click(await within(main).findByRole("button", { name: "Show all of immich's settings" }, { timeout: 5000 }));

    // Each container, its role, and the one restarted twice.
    const containers = (await within(main).findAllByRole("columnheader", { name: "State" }, { timeout: 5000 }))[0].closest("table")!;
    for (const name of ["server", "machine-learning", "redis", "database"]) expect(within(containers).getByText(name)).toBeTruthy();
    expect(within(containers).getByText("web page")).toBeTruthy();
    expect(within(containers).getAllByText("others wait for it")).toHaveLength(2);
    expect(within(containers).getByText("2 restarts")).toBeTruthy();
    // Each container's definition, and Roll back offered for the database's earlier image.
    expect(within(main).getByRole("button", { name: "Copy the container definition of redis" })).toBeTruthy();
    expect(within(main).getByRole("button", { name: /Roll back/ })).toBeTruthy();

    // Change the database's image only.
    fireEvent.click(within(main).getByRole("button", { name: "Change the image of database" }));
    const input = within(main).getByLabelText("Image to run") as HTMLInputElement;
    expect(input.value).toBe("ghcr.io/immich-app/postgres:14-vectorchord0.4.3-pgvectors0.2.0");
    expect(within(main).getByText("Image for database to run")).toBeTruthy();
    fireEvent.change(input, { target: { value: "ghcr.io/immich-app/postgres:15-vectorchord0.4.3-pgvectors0.2.0" } });
    fireEvent.click(within(main).getByRole("button", { name: "Change and restart" }));
    expect(await screen.findByText(/immich: database now runs ghcr.io\/immich-app\/postgres:15/, {}, { timeout: 5000 })).toBeTruthy();

    // One container's logs open on the Logs tab.
    fireEvent.click(within(main).getByRole("button", { name: "Logs of redis" }));
    const log = await within(main).findByRole("log", { name: "Alder: immich: redis" }, { timeout: 5000 });
    expect(await within(log).findAllByText(/immich-redis\[\d+\]: DB saved on disk/, {}, { timeout: 5000 })).not.toHaveLength(0);
    expect(within(log).queryByText(/immich-server/)).toBeNull();
  }, 30_000);
});
