// @vitest-environment jsdom
import "@/test/dom";
import { describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { MotionConfig } from "motion/react";
import { create } from "@bufbuild/protobuf";
import { ServiceSpecSchema, ServiceStatusSchema } from "@/gen/maitre/v1/service_pb";
import type { FleetService } from "@/fleet/useFleet";
import { ServiceFlyout } from "./ServiceFlyout";

describe("ServiceFlyout", () => {
  it("copies the Service's address from the Open row without opening it", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
    const onItem = vi.fn();
    const service: FleetService = {
      spec: create(ServiceSpecSchema, { name: "jellyfin", catalogId: "jellyfin", nodeId: "a" }),
      status: create(ServiceStatusSchema, { url: "http://jellyfin.local/", state: 3 }),
      word: "Online",
      nodeUnreachable: false,
    };
    render(
      <MotionConfig skipAnimations>
        <ServiceFlyout service={service} top={0} collapsed={false} onItem={onItem} onEnter={() => {}} onLeave={() => {}} />
      </MotionConfig>,
    );
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Copy http://jellyfin.local/" }));
    });
    expect(writeText).toHaveBeenCalledWith("http://jellyfin.local/");
    expect(onItem).not.toHaveBeenCalled();
  });
});
