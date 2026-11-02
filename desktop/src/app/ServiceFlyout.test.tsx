// @vitest-environment jsdom
import "@/test/dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { MotionConfig } from "motion/react";
import { create } from "@bufbuild/protobuf";
import { ServiceSpecSchema, ServiceStatusSchema } from "@/gen/maitre/v1/service_pb";
import type { FleetService } from "@/fleet/useFleet";
import { TooltipProvider } from "@/components/ui/tooltip";
import { ServiceFlyout } from "./ServiceFlyout";
import { handleFrameMessage, registerFrameElement, resetFrames, showFrame } from "./frames";

const service: FleetService = {
  spec: create(ServiceSpecSchema, { name: "jellyfin", catalogId: "jellyfin", nodeId: "a" }),
  status: create(ServiceStatusSchema, { url: "http://jellyfin.local/", state: 3 }),
  word: "Online",
  nodeUnreachable: false,
};
const HOME = "http://127.0.0.1:4100/";

function renderFlyout(onItem = vi.fn()) {
  render(
    <MotionConfig skipAnimations>
      <TooltipProvider>
        <ServiceFlyout service={service} top={0} collapsed={false} onItem={onItem} onEnter={() => {}} onLeave={() => {}} />
      </TooltipProvider>
    </MotionConfig>,
  );
  return onItem;
}

describe("ServiceFlyout", () => {
  beforeEach(() => resetFrames());

  it("copies the page's address, said in the Service's own address, without opening it (286)", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
    const win = { postMessage: vi.fn() };
    showFrame("jellyfin", HOME);
    registerFrameElement("jellyfin", { contentWindow: win } as unknown as HTMLIFrameElement);
    const onItem = renderFlyout();
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Copy address" }));
    });
    expect(writeText).toHaveBeenCalledWith("http://jellyfin.local/");
    act(() => handleFrameMessage(win, { maitre: "frame", event: "load", url: `${HOME}web/movies?sort=name` }));
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Copy address" }));
    });
    expect(writeText).toHaveBeenLastCalledWith("http://jellyfin.local/web/movies?sort=name");
    expect(onItem).not.toHaveBeenCalled();
  });

  it("the browser controls follow the frame: Back once there is history, Stop while loading, Home always (286)", async () => {
    const win = { postMessage: vi.fn() };
    showFrame("jellyfin", HOME);
    registerFrameElement("jellyfin", { contentWindow: win } as unknown as HTMLIFrameElement);
    renderFlyout();
    const btn = (name: string) => screen.getByRole("button", { name }) as HTMLButtonElement;
    // Mounted and loading, but the page has not spoken: nothing can be sent to it.
    expect(btn("Back").disabled).toBe(true);
    expect(btn("Stop").disabled).toBe(true);
    expect(btn("Home").disabled).toBe(false);
    act(() => handleFrameMessage(win, { maitre: "frame", event: "load", url: HOME }));
    act(() => handleFrameMessage(win, { maitre: "frame", event: "push", url: `${HOME}web/movies` }));
    expect(btn("Back").disabled).toBe(false);
    expect(btn("Forward").disabled).toBe(true);
    expect(btn("Refresh").disabled).toBe(false);
    fireEvent.click(btn("Back"));
    expect(win.postMessage).toHaveBeenLastCalledWith({ maitre: "frame-cmd", cmd: "go", url: HOME }, "http://127.0.0.1:4100");
    expect(btn("Stop").disabled).toBe(false);
    fireEvent.click(btn("Stop"));
    expect(win.postMessage).toHaveBeenLastCalledWith({ maitre: "frame-cmd", cmd: "stop" }, "http://127.0.0.1:4100");
    expect(screen.getByRole("button", { name: "Refresh" })).toBeTruthy();
  });
});
