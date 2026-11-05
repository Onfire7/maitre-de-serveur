// @vitest-environment jsdom
import "@/test/dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MotionConfig } from "motion/react";
import { createClient } from "@connectrpc/connect";
import { ServiceService } from "@/gen/maitre/v1/service_pb";
import { TooltipProvider } from "@/components/ui/tooltip";
import { DemoFleet, NODE_B } from "@/demo/fleet";
import { createDemoTransport } from "@/demo/transport";
import { useFleetServices, type FleetSnapshot } from "@/fleet/useFleet";
import { RouteProvider } from "./routes";
import { SetupPage } from "./SetupPage";

const B = "10.0.0.6:7443";
let fleet: DemoFleet;

// The page talks to the Node through the real client; the demo Fleet answers.
vi.mock("@/transport", () => ({
  transportFor: () => createDemoTransport(B, fleet),
  noteAddresses: () => {},
}));

function services() {
  const snap = { nodes: fleet.nodes.map((n) => ({ nodeId: n.nodeId, name: n.name })), status: new Map(), records: [], spaces: [] } as unknown as FleetSnapshot;
  // The Fleet's Services as the Console lists them, without React.
  return fleet.services.map((s) => ({ spec: s.spec, status: fleet.status(s), word: "Online" as const, nodeUnreachable: false, node: snap.nodes.find((n) => n.nodeId === s.spec.nodeId) as never }));
}

function renderSetup(name: string) {
  const all = services();
  const service = all.find((s) => s.spec.name === name)!;
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <MotionConfig skipAnimations>
      <QueryClientProvider client={client}>
        <TooltipProvider>
          <RouteProvider initial={{ name: "setup", service: name }}>
            <SetupPage service={service} services={all} target={{ addr: B, fingerprint: NODE_B }} onDone={() => {}} />
          </RouteProvider>
        </TooltipProvider>
      </QueryClientProvider>
    </MotionConfig>,
  );
  return service;
}

describe("the Setup page (ADR 0010)", () => {
  beforeEach(() => {
    fleet = new DemoFleet();
    // Unused, but keeps the lint of the import honest.
    void useFleetServices;
  });

  it("shows the Service's network, its address and the unit the Node writes", async () => {
    renderSetup("qbittorrent");
    expect((screen.getByRole("radio", { name: /Through a Tunnel/ }) as HTMLInputElement).checked).toBe(true);
    expect((screen.getByLabelText("Its address on the Fleet's network") as HTMLInputElement).value).toBe("10.90.0.17");
    await waitFor(() => expect(screen.getByText("qbittorrent.container")).toBeTruthy());
    await waitFor(() => expect(document.querySelector("pre")?.textContent).toContain("Network=container:vpn"));
    // The person's own line is marked as theirs, the agent's as the agent's.
    const line = [...document.querySelectorAll("pre div")].find((d) => d.textContent === "Environment=WEBUI_PORT=8080");
    expect(line?.getAttribute("data-source")).toBe("override");
    const label = [...document.querySelectorAll("pre div")].find((d) => d.textContent === "Label=dev.maitre.service=qbittorrent");
    expect(label?.getAttribute("data-source")).toBe("agent");
  });

  it("Apply is offered only once something changed, and says what restarts", async () => {
    renderSetup("qbittorrent");
    const apply = () => screen.getByRole("button", { name: /Apply and restart/ });
    await waitFor(() => expect((apply() as HTMLButtonElement).disabled).toBe(true));
    await act(async () => {
      fireEvent.click(screen.getByRole("radio", { name: /Its own network/ }));
    });
    await waitFor(() => expect((apply() as HTMLButtonElement).disabled).toBe(false));
    await waitFor(() => expect(screen.getByText(/Applying restarts qbittorrent and vpn/)).toBeTruthy());
    // The unit loses the Tunnel's line and takes the shared network's.
    await waitFor(() => expect(document.querySelector("pre")?.textContent).toContain("Network=maitre.network"));
    const added = [...document.querySelectorAll('pre div[data-mark="added"]')].map((d) => d.textContent);
    const removed = [...document.querySelectorAll('pre div[data-mark="removed"]')].map((d) => d.textContent);
    expect(added.join("\n")).toContain("Network=maitre.network:ip=10.90.0.17");
    expect(removed.join("\n")).toContain("Network=container:vpn");
  });

  it("says what the Node refuses, and Apply waits", async () => {
    renderSetup("qbittorrent");
    await act(async () => {
      fireEvent.change(screen.getByLabelText("Its address on the Fleet's network"), { target: { value: "10.90.0.10" } });
    });
    await waitFor(() => expect(screen.getByText(/already has the address 10\.90\.0\.10/)).toBeTruthy());
    expect((screen.getByRole("button", { name: /Apply and restart/ }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("applies a change to the Node", async () => {
    renderSetup("qbittorrent");
    await waitFor(() => expect((screen.getByRole("button", { name: /Apply and restart/ }) as HTMLButtonElement).disabled).toBe(true));
    await act(async () => {
      fireEvent.click(screen.getByRole("switch", { name: /Reachable by other Services/ }));
    });
    await waitFor(() => expect((screen.getByRole("button", { name: /Apply and restart/ }) as HTMLButtonElement).disabled).toBe(false));
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: /Apply and restart/ }));
    });
    await waitFor(() => expect(fleet.services.find((s) => s.spec.name === "qbittorrent")!.spec.fleetReach).toBe(true));
    const got = await createClient(ServiceService, createDemoTransport(B, fleet)).previewSetup({ name: "qbittorrent" });
    expect(got.units.length).toBeGreaterThan(0);
  });

  it("a Tunnel keeps its own network and says so", async () => {
    renderSetup("vpn");
    expect(screen.getByText(/This is a Tunnel/)).toBeTruthy();
    expect(screen.queryByRole("radio", { name: /Through a Tunnel/ })).toBeNull();
    expect(screen.getByText(/qbittorrent is on this Service's network/)).toBeTruthy();
    // It offers no page, so its Pages card is empty rather than absent.
    expect(screen.getByText("No pages.")).toBeTruthy();
  });
});
