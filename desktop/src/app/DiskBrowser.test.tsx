// @vitest-environment jsdom
// Looking inside a disk of a machine being set up (decision 262), on the
// demo Installer, and against an Installer from before ListFolder.
import "@/test/dom";
import { describe, expect, it } from "vitest";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { Code, ConnectError, createClient, type Transport } from "@connectrpc/connect";
import { DemoFleet } from "@/demo/fleet";
import { createDemoTransport } from "@/demo/transport";
import { InstallerService } from "@/gen/maitre/v1/installer_pb";
import { DiskBrowser, holds } from "./DiskBrowser";

/** Opens a folder of the listing by its name. */
async function open(name: string) {
  fireEvent.click((await screen.findByText(name, { selector: "li span" })).closest("button")!);
}

async function demoDisks(fleet: DemoFleet) {
  const client = createClient(InstallerService, createDemoTransport(fleet.installer.addr, fleet));
  return { client, disks: (await client.getInstallerStatus({})).status!.disks };
}

describe("DiskBrowser", () => {
  it("opens the largest partition, goes into folders and back", async () => {
    const fleet = new DemoFleet();
    const { client, disks } = await demoDisks(fleet);
    const ssd = disks.find((d) => d.path === "/dev/nvme0n1")!;
    expect(holds(ssd)).toBe("Windows (ntfs)");
    render(<DiskBrowser client={client} disk={ssd} />);

    await open("Users");
    await open("sam");
    expect(await screen.findByText("NTUSER.DAT")).toBeTruthy();
    // The partition with no filesystem cannot be chosen, and says why.
    const reserved = screen.getByRole("button", { name: /Microsoft reserved partition/ }) as HTMLButtonElement;
    expect(reserved.disabled).toBe(true);
    expect(reserved.textContent).toContain("no filesystem");
    // The path back up.
    fireEvent.click(screen.getByRole("button", { name: "Windows" }));
    expect(await screen.findByText("pagefile.sys")).toBeTruthy();
    // Another partition starts at its top.
    fireEvent.click(screen.getByRole("button", { name: /EFI system partition/ }));
    expect(await screen.findByText("EFI", { selector: "li span" })).toBeTruthy();
  });

  it("says an older Installer cannot look inside disks", async () => {
    const fleet = new DemoFleet();
    const t = createDemoTransport(fleet.installer.addr, fleet);
    const older = { ...t, unary: (method, ...rest) => (method.name === "ListFolder" ? Promise.reject(new ConnectError("not registered", Code.Unimplemented)) : t.unary(method, ...rest)) } as Transport;
    const client = createClient(InstallerService, older);
    const { disks } = await demoDisks(fleet);
    render(<DiskBrowser client={client} disk={disks.find((d) => d.path === "/dev/sdb")!} />);
    expect(await screen.findByText(/too old to show what is on its disks/)).toBeTruthy();
  });

  it("lists files with their sizes", async () => {
    const fleet = new DemoFleet();
    const { client, disks } = await demoDisks(fleet);
    const { container } = render(<DiskBrowser client={client} disk={disks.find((d) => d.path === "/dev/sdb")!} />);
    await open("Films");
    const row = (await screen.findByText("Arrival (2016).mkv")).closest("li")!;
    expect(within(row).getByText("8.1 GB")).toBeTruthy();
    expect(container.textContent).toContain("Read-only");
  });
});
