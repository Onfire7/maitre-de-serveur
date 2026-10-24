// Connect transports that route every RPC through the Tauri `rpc` command,
// so the Rust side can speak mutual TLS with the Node. The webview never
// holds a key and never talks TLS itself.
//
// The target is encoded in the base URL as https://<fingerprint>@<host:port>;
// tauriFetch pulls both out and hands them to Rust, which pins the key.
import { invoke } from "@tauri-apps/api/core";
import { createConnectTransport } from "@connectrpc/connect-web";
import type { Transport } from "@connectrpc/connect";

interface RpcResponse {
  status: number;
  content_type: string;
  body: number[];
}

export interface Target {
  addr: string;
  fingerprint: string;
}

async function tauriFetch(input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
  const url = typeof input === "string" ? new URL(input) : input instanceof URL ? input : new URL(input.url);
  const raw = init?.body;
  let body: Uint8Array;
  if (raw instanceof Uint8Array) body = raw;
  else if (raw instanceof ArrayBuffer) body = new Uint8Array(raw);
  else if (typeof raw === "string") body = new TextEncoder().encode(raw);
  else if (raw == null) body = new Uint8Array();
  else body = new Uint8Array(await new Response(raw as BodyInit).arrayBuffer());

  const res = await invoke<RpcResponse>("rpc", {
    addr: url.host,
    fingerprint: decodeURIComponent(url.username),
    procedure: url.pathname,
    body: Array.from(body),
  });
  return new Response(new Uint8Array(res.body), {
    status: res.status,
    headers: { "content-type": res.content_type },
  });
}

const cache = new Map<string, Transport>();

/** A transport pinned to one Node or Installer. */
export function transportFor(t: Target): Transport {
  const key = `${t.fingerprint}@${t.addr}`;
  let tr = cache.get(key);
  if (!tr) {
    tr = createConnectTransport({
      baseUrl: `https://${encodeURIComponent(t.fingerprint)}@${t.addr}`,
      useBinaryFormat: true,
      fetch: tauriFetch,
    });
    cache.set(key, tr);
  }
  return tr;
}
