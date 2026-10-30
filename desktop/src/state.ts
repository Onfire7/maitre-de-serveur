// Mirrors the Rust ConsoleState / commands in src-tauri/src/lib.rs.
import { invoke } from "@tauri-apps/api/core";
import { isDemo } from "./demo/flag";
import { demoApi } from "./demo/api";

export interface FleetState {
  fleet_id: string;
  name: string;
  created_unix_ms: number;
  recovery_public_key_pem: string;
}
export interface KnownNode {
  node_id: string;
  name: string;
  addresses: string[];
}
export interface ConsoleState {
  fleet: FleetState | null;
  nodes: KnownNode[];
}
export interface DeviceInfo {
  fingerprint: string;
  public_key_pem: string;
  public_key_path: string;
  name: string;
}
export interface Found {
  kind: "installer" | "node" | "depot" | "device";
  id: string;
  name: string;
  machine: string;
  fleet_id: string;
  version: string;
  addresses: string[];
  key_pem: string;
  recovery: boolean;
}
export interface Peek {
  fingerprint: string;
  pairing_code: string;
  public_key_pem: string;
}

const tauriApi = {
  deviceInfo: () => invoke<DeviceInfo>("device_info"),
  getState: () => invoke<ConsoleState>("get_state"),
  setState: (newState: ConsoleState) => invoke<void>("set_state", { newState }),
  createFleet: (name: string) => invoke<string>("create_fleet", { name }),
  discover: () => invoke<Found[]>("discover"),
  peekInstaller: (addr: string) => invoke<Peek>("peek_installer", { addr }),
  /** Learn the key of whatever answers at addr (a Node too): the TLS handshake shows it. */
  peekKey: (addr: string) => invoke<Peek>("peek_installer", { addr }),
};

/** The Console's own state and LAN discovery; the demo Fleet's when demo mode is on. */
export const api: typeof tauriApi = isDemo() ? demoApi : tauriApi;
