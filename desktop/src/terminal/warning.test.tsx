// @vitest-environment jsdom
import "@/test/dom";
import { describe, expect, it } from "vitest";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { create } from "@bufbuild/protobuf";
import { TerminalInfoSchema } from "@/gen/maitre/v1/terminal_pb";
import { EndsTerminals, terminalWarning, useEndingConfirm } from "./warning";

const held = (service: string, container: string, argv: string[], running = true) => create(TerminalInfoSchema, { id: `${service}-${argv.join("-")}`, service, container, user: "root", argv, running });
const target = { addr: "127.0.0.1:7443", fingerprint: "ab".repeat(32) };

describe("the warning before an action that ends running Terminals (ADR 0011)", () => {
  it("names the one running in the Service by its container and command", () => {
    expect(terminalWarning([held("nextcloud", "server", ["app", "migrate"])])).toEqual(["A Terminal is running in server: `app migrate`. It will end."]);
  });

  it("names each of several, and a shell as a shell", () => {
    expect(terminalWarning([held("nextcloud", "server", ["app", "migrate"]), held("nextcloud", "db", [])])).toEqual(["2 Terminals are running, and will end:", "in server: `app migrate`", "in db: a shell"]);
  });

  it("names the Service too when it covers a whole Node", () => {
    expect(terminalWarning([held("nextcloud", "server", ["occ", "upgrade"]), held("kuma", "kuma", ["/bin/bash"])], { node: true })).toEqual([
      "2 Terminals are running on this Node, and will end:",
      "in nextcloud (server): `occ upgrade`",
      "in kuma: `/bin/bash`",
    ]);
  });

  it("names the Service when it covers several", () => {
    expect(terminalWarning([held("nextcloud", "server", ["occ", "upgrade"])], { services: true })).toEqual(["A Terminal is running in nextcloud (server): `occ upgrade`. It will end."]);
  });

  it("leaves finished Terminals out, and says nothing when none runs", () => {
    expect(terminalWarning([held("kuma", "kuma", ["ls"], false)])).toEqual([]);
  });

  it("shows in a confirmation once the Node has listed them, without holding the action back", async () => {
    let asked = "";
    render(
      <EndsTerminals
        target={target}
        services={["nextcloud"]}
        list={async (_t, service) => {
          asked = service;
          return [held("nextcloud", "server", ["app", "migrate"]), held("kuma", "kuma", ["htop"])];
        }}
      />,
    );
    const note = await screen.findByRole("note");
    expect(note.textContent).toBe("A Terminal is running in server: app migrate. It will end.");
    expect(note.querySelector("code")?.textContent).toBe("app migrate");
    expect(asked).toBe("");
  });

  it("shows nothing when the Node cannot be asked", async () => {
    const { container } = render(<EndsTerminals target={target} list={async () => Promise.reject(new Error("unimplemented"))} />);
    await new Promise((r) => setTimeout(r, 0));
    expect(container.textContent).toBe("");
  });
});

describe("Stop, Restart and Update ask first only while a Terminal runs", () => {
  function Harness({ list, done }: { list: (t: typeof target, service: string) => Promise<ReturnType<typeof held>[]>; done: (go: boolean) => void }) {
    const [confirm, dialog] = useEndingConfirm(list);
    return (
      <>
        <button onClick={() => confirm({ title: "Stop nextcloud?", action: "Stop", groups: [{ target, services: ["nextcloud"] }] }).then(done)}>stop</button>
        {dialog}
      </>
    );
  }
  const flush = () => act(() => new Promise((r) => setTimeout(r, 0)));

  it("goes ahead at once when none is running", async () => {
    const answers: boolean[] = [];
    render(<Harness list={async () => [held("kuma", "kuma", ["htop"])]} done={(go) => answers.push(go)} />);
    fireEvent.click(screen.getByText("stop"));
    await flush();
    expect(answers).toEqual([true]);
    expect(screen.queryByRole("alertdialog")).toBeNull();
  });

  it("names the running Terminal, and the action can still go ahead", async () => {
    const answers: boolean[] = [];
    render(<Harness list={async () => [held("nextcloud", "server", ["app", "migrate"])]} done={(go) => answers.push(go)} />);
    fireEvent.click(screen.getByText("stop"));
    const dialog = await screen.findByRole("alertdialog");
    expect(dialog.textContent).toContain("Stop nextcloud?");
    expect(dialog.textContent).toContain("A Terminal is running in server: app migrate. It will end.");
    fireEvent.click(screen.getByRole("button", { name: "Stop" }));
    await flush();
    expect(answers).toEqual([true]);
  });

  it("does nothing when cancelled", async () => {
    const answers: boolean[] = [];
    render(<Harness list={async () => [held("nextcloud", "server", ["app", "migrate"])]} done={(go) => answers.push(go)} />);
    fireEvent.click(screen.getByText("stop"));
    await screen.findByRole("alertdialog");
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    await flush();
    expect(answers).toEqual([false]);
  });
});
