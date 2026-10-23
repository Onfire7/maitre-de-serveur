// Connect transport that routes every RPC through the Tauri `rpc` command,
// so the Rust side can speak mutual TLS with the Node. The webview never
// holds a key and never talks TLS itself.
import { invoke } from "@tauri-apps/api/core";
import { createConnectTransport } from "@connectrpc/connect-web";

interface RpcResponse {
  status: number;
  content_type: string;
  body: number[];
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

  const res = await invoke<RpcResponse>("rpc", { procedure: url.pathname, body: Array.from(body) });
  return new Response(new Uint8Array(res.body), {
    status: res.status,
    headers: { "content-type": res.content_type },
  });
}

// baseUrl is only used to build the path; the host is ignored by tauriFetch.
export const transport = createConnectTransport({
  baseUrl: "https://node",
  useBinaryFormat: true,
  fetch: tauriFetch,
});
