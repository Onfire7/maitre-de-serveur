// A Node typed in by address (decision 219), for when discovery finds
// nothing: another subnet, a VPN, or a network that drops multicast.
import type { Found } from "@/state";

export const NODE_PORT = 7443;

/** "192.168.1.20", "fe80::1", "https://nas.lan:7443/" → host:port, or "" if it is not an address. */
export function nodeAddr(input: string): string {
  let s = input.trim().replace(/^[a-z]+:\/\//i, "").replace(/\/.*$/, "");
  if (!s) return "";
  let host = s;
  let port = String(NODE_PORT);
  const v6 = s.match(/^\[([0-9a-f:.%]+)\](?::(\d+))?$/i);
  if (v6) [host, port] = [v6[1], v6[2] ?? port];
  else if ((s.match(/:/g) ?? []).length > 1) host = s;
  else if (s.includes(":")) [host, port] = s.split(":");
  if (!/^\d{1,5}$/.test(port) || +port < 1 || +port > 65535) return "";
  if (host.includes(":")) return /^[0-9a-f:.%]+$/i.test(host) ? `[${host}]:${port}` : "";
  return /^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$/i.test(host) ? `${host}:${port}` : "";
}

/** A Node reached by address, shaped like one discovery found. */
export function typedNode(addr: string, fingerprint: string): Found {
  return { kind: "node", id: fingerprint, name: addr.replace(/:\d+$/, "").replace(/^\[|\]$/g, ""), machine: "", fleet_id: "", version: "", addresses: [addr], key_pem: "", recovery: true };
}

/** Discovered Nodes plus typed ones; a typed address joins a discovered Node with the same key. */
export function withTyped(found: Found[], typed: Found[]): Found[] {
  const out = found.map((f) => ({ ...f, addresses: [...f.addresses] }));
  for (const t of typed) {
    const same = out.find((f) => f.kind === "node" && f.id === t.id);
    if (!same) out.push(t);
    else if (!same.addresses.includes(t.addresses[0])) same.addresses.unshift(t.addresses[0]);
  }
  return out;
}
