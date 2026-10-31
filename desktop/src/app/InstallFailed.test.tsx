// @vitest-environment jsdom
// An Install the Installer gave up on shows what the failing command printed
// (decision 265); an older Installer sends no output and the message stands alone.
import "@/test/dom";
import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { create } from "@bufbuild/protobuf";
import { ProvisioningSchema, Provisioning_Phase } from "@/gen/maitre/v1/installer_pb";
import { InstallFailed } from "./AddNode";

describe("InstallFailed", () => {
  it("offers the failing command's output", () => {
    const prov = create(ProvisioningSchema, {
      phase: Provisioning_Phase.FAILED,
      message: "bootc install: exit status 1: ERROR Installing to disk: Task bootupctl failed",
      output: ["Running bootupctl backend install", "ERROR Installing to disk: Task bootupctl failed", "Command {", "}"],
    });
    const { container } = render(<InstallFailed prov={prov} />);
    expect(screen.getByText(/Install failed: bootc install/)).toBeTruthy();
    expect(screen.getByText("What the installer printed (4 lines)")).toBeTruthy();
    expect(container.querySelector("pre")!.textContent).toContain("Running bootupctl backend install");
  });

  it("shows only the message from an older Installer", () => {
    const prov = create(ProvisioningSchema, { phase: Provisioning_Phase.FAILED, message: "sfdisk: out of room" });
    const { container } = render(<InstallFailed prov={prov} />);
    expect(screen.getByText("Install failed: sfdisk: out of room")).toBeTruthy();
    expect(container.querySelector("details")).toBeNull();
  });
});
