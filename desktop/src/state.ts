// Mirrors the Rust ConsoleState / commands in src-tauri/src/lib.rs.
import { invoke } from "@tauri-apps/api/core";

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
  kind: "installer" | "node" | "depot";
  id: string;
  name: string;
  machine: string;
  fleet_id: string;
  version: string;
  addresses: string[];
}
export interface Peek {
  fingerprint: string;
  pairing_code: string;
  public_key_pem: string;
}

export const api = {
  deviceInfo: () => invoke<DeviceInfo>("device_info"),
  getState: () => invoke<ConsoleState>("get_state"),
  setState: (newState: ConsoleState) => invoke<void>("set_state", { newState }),
  createFleet: (name: string) => invoke<string>("create_fleet", { name }),
  discover: () => invoke<Found[]>("discover"),
  peekInstaller: (addr: string) => invoke<Peek>("peek_installer", { addr }),
};
