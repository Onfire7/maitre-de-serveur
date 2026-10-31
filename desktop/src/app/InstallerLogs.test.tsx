// @vitest-environment jsdom
// The Installer's log over the network (decision 271), on the demo
// Installer, and from an Installer without GetLogs.
import "@/test/dom";
import { describe, expect, it } from "vitest";
import { Code, ConnectError, createClient, type Transport } from "@connectrpc/connect";
import { DemoFleet } from "@/demo/fleet";
import { createDemoTransport } from "@/demo/transport";
import { InstallerService } from "@/gen/maitre/v1/installer_pb";

describe("GetLogs on the demo Installer", () => {
  it("returns the journal, newest last", async () => {
    const fleet = new DemoFleet();
    const client = createClient(InstallerService, createDemoTransport(fleet.installer.addr, fleet));
    const r = await client.getLogs({ lines: 100 });
    expect(r.lines[0]).toContain("maitre installer");
    expect(r.lines.at(-1)).toContain("listening");
  });

  it("is told apart from an older Installer", async () => {
    const fleet = new DemoFleet();
    const t = createDemoTransport(fleet.installer.addr, fleet);
    const older = { ...t, unary: (method, ...rest) => (method.name === "GetLogs" ? Promise.reject(new ConnectError("not registered", Code.Unimplemented)) : t.unary(method, ...rest)) } as Transport;
    await expect(createClient(InstallerService, older).getLogs({})).rejects.toMatchObject({ code: Code.Unimplemented });
  });
});
