// Connect transports that route every RPC through the Tauri `rpc` command,
// so the Rust side can speak mutual TLS with the Node. The webview never
// holds a key and never talks TLS itself.
//
// The target is encoded in the base URL as https://<fingerprint>@<host:port>;
// tauriFetch pulls both out and hands them to Rust, which pins the key.
import { invoke } from "@tauri-apps/api/core";
import { createConnectTransport } from "@connectrpc/connect-web";
import type { Transport } from "@connectrpc/connect";
import { isDemo } from "./demo/flag";
import { createDemoTransport } from "./demo/transport";
import { demoteRoute, rememberRoute, routeOrder } from "./fleet/route";

interface RpcResponse {
  status: number;
  content_type: string;
  body: number[];
}

export interface Target {
  addr: string;
  fingerprint: string;
}

/** The ways to each Node the Console has seen, by fingerprint, for a call that fails to fall back on. */
const ways = new Map<string, string[]>();

/** Note the ways to a Node, so a call to one address that does not answer can try the others. */
export function noteAddresses(fingerprint: string, addresses: string[]) {
  if (addresses.length) ways.set(fingerprint, addresses);
}

/** The ways to try after `addr` did not answer, in order. */
function alternatives(fingerprint: string, addr: string): string[] {
  const all = ways.get(fingerprint) ?? [];
  if (!all.includes(addr)) return [];
  return routeOrder(fingerprint, all, undefined).filter((a) => a !== addr);
}

/**
 * Send one RPC to the Node. When the address does not answer at all (the
 * Rust side could not reach it) and `fallback` is on, the address goes
 * last in line for this Node and the call tries its other ways in turn,
 * remembering the one that answers; a Node that answered with an error is
 * not retried.
 */
async function send(addr: string, fingerprint: string, procedure: string, body: Uint8Array, fallback: boolean): Promise<RpcResponse> {
  const tried = [addr, ...(fallback ? alternatives(fingerprint, addr) : [])];
  let lastErr: unknown;
  for (const [i, a] of tried.entries()) {
    try {
      const res = await invoke<RpcResponse>("rpc", { addr: a, fingerprint, procedure, body: Array.from(body) });
      if (i > 0) rememberRoute(fingerprint, a);
      return res;
    } catch (e) {
      lastErr = e;
      if (fallback) demoteRoute(fingerprint, a);
    }
  }
  throw lastErr;
}

async function tauriFetch(input: RequestInfo | URL, init?: RequestInit, fallback = true): Promise<Response> {
  const url = typeof input === "string" ? new URL(input) : input instanceof URL ? input : new URL(input.url);
  const raw = init?.body;
  let body: Uint8Array;
  if (raw instanceof Uint8Array) body = raw;
  else if (raw instanceof ArrayBuffer) body = new Uint8Array(raw);
  else if (typeof raw === "string") body = new TextEncoder().encode(raw);
  else if (raw == null) body = new Uint8Array();
  else body = new Uint8Array(await new Response(raw as BodyInit).arrayBuffer());

  const res = await send(url.host, decodeURIComponent(url.username), url.pathname, body, fallback);
  return new Response(new Uint8Array(res.body), {
    status: res.status,
    headers: { "content-type": res.content_type },
  });
}

const cache = new Map<string, Transport>();

/**
 * A transport pinned to one Node or Installer. By default a call whose
 * address does not answer falls back on the Node's other known ways;
 * `fallback: false` is for the Fleet read, which tries every way itself
 * and must know which one answered.
 */
export function transportFor(t: Target, opts: { fallback?: boolean } = {}): Transport {
  const fallback = opts.fallback ?? true;
  const key = `${t.fingerprint}@${t.addr}${fallback ? "" : "#direct"}`;
  let tr = cache.get(key);
  if (!tr && isDemo()) {
    tr = createDemoTransport(t.addr);
    cache.set(key, tr);
  }
  if (!tr) {
    tr = createConnectTransport({
      baseUrl: `https://${encodeURIComponent(t.fingerprint)}@${t.addr}`,
      useBinaryFormat: true,
      fetch: (input, init) => tauriFetch(input, init, fallback),
    });
    cache.set(key, tr);
  }
  return tr;
}
