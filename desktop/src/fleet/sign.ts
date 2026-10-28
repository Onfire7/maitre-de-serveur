// Signed records (decision 189): every record this Console writes carries
// this device's Ed25519 signature over its kind, id, body, deleted flag and
// signing time. The bytes are built and signed in Rust (`sign_record`); the
// webview only passes the fields. A replacement must be signed later than
// the record it replaces, so an old body cannot be replayed over a newer one.
import { invoke } from "@tauri-apps/api/core";
import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { createClient } from "@connectrpc/connect";
import { RecordSchema, type Record as Rec } from "@/gen/maitre/v1/fleet_pb";
import { NodeService } from "@/gen/maitre/v1/node_pb";
import { transportFor, type Target } from "@/transport";
import { isDemo } from "@/demo/flag";

export interface RecordFields {
  kind: string;
  id: string;
  body?: Uint8Array;
  deleted?: boolean;
}

interface SignResult {
  signerKey: number[];
  signature: number[];
  signedUnixMs: number;
}

/** The signing time for a record replacing `prev`: now, or just after `prev` was signed. */
export function nextSignedMs(prev?: Pick<Rec, "signedUnixMs"> | null, now = Date.now()): bigint {
  const after = prev ? prev.signedUnixMs + 1n : 0n;
  const t = BigInt(now);
  return after > t ? after : t;
}

/** The current record of a kind and id in a list, such as a Fleet snapshot's. */
export function currentRecord(records: Rec[] | undefined, kind: string, id: string): Rec | undefined {
  return records?.find((r) => r.kind === kind && r.id === id);
}

/**
 * The record signed by this device, or by the Recovery Phrase's key when a
 * phrase is given. A deleted record goes out with an empty body.
 */
export async function signedRecord(r: RecordFields, opts: { prev?: Rec | null; phrase?: string } = {}): Promise<Rec> {
  const deleted = !!r.deleted;
  const body = deleted ? new Uint8Array() : (r.body ?? new Uint8Array());
  const signedUnixMs = nextSignedMs(opts.prev);
  const init = { kind: r.kind, id: r.id, body, deleted, signedUnixMs } satisfies MessageInitShape<typeof RecordSchema>;
  // The demo Fleet checks nothing; there is no key to sign with in a browser.
  if (isDemo()) return create(RecordSchema, { ...init, signerKey: new Uint8Array(32), signature: new Uint8Array(64) });
  const s = await invoke<SignResult>("sign_record", { kind: r.kind, id: r.id, body: Array.from(body), deleted, signedUnixMs: Number(signedUnixMs), phrase: opts.phrase ?? null });
  return create(RecordSchema, { ...init, signerKey: new Uint8Array(s.signerKey), signature: new Uint8Array(s.signature), signedUnixMs: BigInt(s.signedUnixMs) });
}

/** Sign a record and put it through a Node. `prev` is the record it replaces, when known. */
export async function putSigned(t: Target, r: RecordFields, prev?: Rec | null) {
  const record = await signedRecord(r, { prev });
  return createClient(NodeService, transportFor(t)).putRecord({ record });
}

/** Lowercase hex SHA-256 of the raw Ed25519 key in an SPKI PEM: a device id. */
export async function fingerprintOfPem(pem: string): Promise<string> {
  const b64 = pem
    .split("\n")
    .filter((l) => !l.startsWith("-----"))
    .join("")
    .trim();
  const der = Uint8Array.from(atob(b64), (c) => c.charCodeAt(0));
  if (der.length !== 44) throw new Error("not an Ed25519 public key");
  const sum = new Uint8Array(await crypto.subtle.digest("SHA-256", der.slice(12)));
  return Array.from(sum, (b) => b.toString(16).padStart(2, "0")).join("");
}
