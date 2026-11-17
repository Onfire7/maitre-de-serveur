// @vitest-environment jsdom
import "@/test/dom";
import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { OpenTerminalDialog } from "./OpenTerminalDialog";

describe("opening a Terminal (#7)", () => {
  it("offers a Service's containers, the first chosen, and opens in the one picked", () => {
    const onOpen = vi.fn();
    render(<OpenTerminalDialog service="photos" containers={["server", "database", "redis"]} onOpen={onOpen} onClose={() => {}} />);
    const server = screen.getByRole("radio", { name: "server" });
    expect(server.getAttribute("aria-checked")).toBe("true");
    fireEvent.click(screen.getByRole("radio", { name: "database" }));
    fireEvent.click(screen.getByRole("button", { name: "Open" }));
    expect(onOpen).toHaveBeenCalledWith({ component: "database", root: false });
  });

  it("runs as the image's user unless Run as root is ticked", () => {
    const onOpen = vi.fn();
    render(<OpenTerminalDialog service="kuma" containers={[]} onOpen={onOpen} onClose={() => {}} />);
    // One container: nothing to pick.
    expect(screen.queryByRole("radio")).toBeNull();
    const root = screen.getByRole("checkbox", { name: /Run as root/ });
    expect(root.getAttribute("aria-checked")).toBe("false");
    fireEvent.click(root);
    fireEvent.click(screen.getByRole("button", { name: "Open" }));
    expect(onOpen).toHaveBeenCalledWith({ component: "", root: true });
  });
});
