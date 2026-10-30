// The Tauri-side Console state and discovery, answered from the demo Fleet
// (decision 153). Mirrors `api` in ../state.ts.
import type { ConsoleState, DeviceInfo, Found, Peek } from "@/state";
import { DEVICE_ID, demoFleet } from "./fleet";

let state: ConsoleState | undefined;

function fromFleet(): ConsoleState {
  const f = demoFleet();
  return {
    fleet: { fleet_id: f.records.find((r) => r.kind === "fleet")?.id ?? "", name: f.fleetName, created_unix_ms: f.now - 30 * 86400_000, recovery_public_key_pem: "demo" },
    nodes: f.nodes.map((n) => ({ node_id: n.nodeId, name: n.name, addresses: [n.addr, `${n.name.toLowerCase()}.local:7443`] })),
  };
}

export const demoApi = {
  deviceInfo: async (): Promise<DeviceInfo> => ({ fingerprint: DEVICE_ID, public_key_pem: "demo", public_key_path: "/demo/device.pem", name: "Sean's laptop" }),
  getState: async (): Promise<ConsoleState> => (state ??= fromFleet()),
  setState: async (s: ConsoleState) => {
    state = s;
  },
  createFleet: async (name: string) => {
    const f = demoFleet();
    f.fleetName = name;
    f.rebuildRecords();
    state = fromFleet();
    return state.fleet!.fleet_id;
  },
  discover: async (): Promise<Found[]> => {
    const f = demoFleet();
    const out: Found[] = f.nodes
      .filter((n) => n.reachable)
      .map((n) => ({ kind: "node", id: n.nodeId, name: n.name, machine: "", fleet_id: state?.fleet?.fleet_id ?? "", version: "0.9.3", addresses: [n.addr], key_pem: "demo", recovery: false }));
    if (!f.installer.provisioning) out.push({ kind: "installer", id: f.installer.id, name: f.installer.name, machine: f.installer.machine, fleet_id: "", version: "0.9.3", addresses: [f.installer.addr], key_pem: "demo", recovery: false });
    if (f.pendingDevice.id) out.push({ kind: "device", id: f.pendingDevice.id, name: f.pendingDevice.name, machine: f.pendingDevice.machine, fleet_id: "", version: "", addresses: ["10.0.0.23:7444"], key_pem: "demo", recovery: false });
    return out;
  },
  peekInstaller: async (addr: string): Promise<Peek> => {
    const f = demoFleet();
    if (addr !== f.installer.addr) throw new Error(`nothing answers at ${addr}`);
    return { fingerprint: f.installer.id, pairing_code: f.installer.pairingCode, public_key_pem: "demo" };
  },
  peekKey: async (addr: string): Promise<Peek> => {
    const f = demoFleet();
    const n = f.nodeByAddr(addr);
    if (n) return { fingerprint: n.nodeId, pairing_code: "", public_key_pem: "demo" };
    return demoApi.peekInstaller(addr);
  },
};
