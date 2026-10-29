// Grants (decision 189): a Service whose container definition has a
// protected option runs only once a Trusted Device has signed a grant for
// that option. The Node answers a Service call that needs one with
// FailedPrecondition and a GrantsNeeded detail; the Console asks, signs a
// grant record per option, and tries the call once more.
import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import { Code, ConnectError, createClient } from "@connectrpc/connect";
import { GrantSchema, GrantsNeededSchema, type Grant, type GrantsNeeded, type Record as Rec } from "@/gen/maitre/v1/fleet_pb";
import { NodeService } from "@/gen/maitre/v1/node_pb";
import { api } from "@/state";
import { transportFor, type Target } from "@/transport";
import { currentRecord, putSigned } from "./sign";
import { grantMatches, type CatalogEntry, type CatalogGrant } from "@/services";

/** What a protected option lets the container do, in plain words. */
export function optionNote(option: string): string {
  const o = option.trim();
  const section = /^\[(\w+)\]\s*(.*)$/.exec(o);
  if (section && section[1] !== "Container") return `Runs ${section[2] ? `"${section[2]}"` : "a command"} as root on the Node itself, outside the container.`;
  const [key, ...rest] = o.split("=");
  const value = rest.join("=");
  switch (key) {
    case "AddCapability":
      return `Gives the container the Linux capability ${value || "named"}: a part of root's power over the Node${/SYS_ADMIN|ALL/.test(value) ? ", here nearly all of it" : ""}.`;
    case "AddDevice":
      return `Direct access to the Node's hardware${value ? ` at ${value}` : ""}, bypassing the operating system's checks.`;
    case "Network":
      return value === "host" ? "Uses the Node's own network: it can see and answer on every network connection of the Node, and its page is open to your home network at the Node's address, whatever Reachable says." : `Joins the network "${value}" instead of the Service's own.`;
    case "Volume":
      return `Reads and writes ${value ? value.split(":")[0] : "a folder"} on the Node, outside its Storage Spaces.`;
    case "PodmanArgs":
    case "GlobalArgs":
      return `Raw Podman arguments (${value || "any"}): they can switch off any protection.`;
    case "SecurityLabelDisable":
      return "Turns off SELinux, a layer of the sandbox that keeps the container away from the Node's files.";
    case "SeccompProfile":
      return "Turns off or replaces the seccomp filter, a layer of the sandbox that blocks dangerous system calls.";
    case "Sysctl":
      return `Changes a kernel setting (${value || "sysctl"}) of the Node.`;
    case "Privileged":
    case "privileged":
      return "Runs the container with all of root's powers over the Node.";
    case "Pid":
    case "Ipc":
    case "UserNS":
      return `Shares the Node's own ${key === "UserNS" ? "user" : key.toUpperCase()} namespace with the container.`;
    case "SecurityLabelType":
    case "SecurityLabelLevel":
    case "SecurityLabelFileType":
      return "Changes the SELinux label that confines the container.";
    default:
      return "Lifts a limit Maître puts on containers.";
  }
}

/** The grants a Service holds, from the Fleet's records. */
export function grantsOf(records: Rec[] | undefined, service: string): { record: Rec; grant: Grant }[] {
  return (records ?? [])
    .filter((r) => r.kind === "grant" && !r.deleted)
    .map((record) => ({ record, grant: fromBinary(GrantSchema, record.body) }))
    .filter((g) => g.grant.service === service)
    .sort((a, b) => a.grant.component.localeCompare(b.grant.component) || a.grant.option.localeCompare(b.grant.option));
}

/** The GrantsNeeded detail of an error, if it is one. */
export function grantsNeeded(e: unknown): GrantsNeeded | undefined {
  if (!(e instanceof ConnectError) || e.code !== Code.FailedPrecondition) return undefined;
  const d = e.findDetails(GrantsNeededSchema)[0];
  return d && d.options.length > 0 ? d : undefined;
}

/** The person declined the warning; callers stay quiet. */
export class GrantsDeclined extends Error {
  constructor() {
    super("not granted");
    this.name = "GrantsDeclined";
  }
}

export function declined(e: unknown): boolean {
  return e instanceof GrantsDeclined;
}

/**
 * The answer to a question about grants: allow all, decline, or (for a
 * Catalog entry that declares optional features, G8) install without some.
 */
export type GrantAnswer = boolean | { without: string[] };

/** Asks; entry is given when the entry declares every option asked for. */
type Ask = (need: GrantsNeeded, entry?: CatalogEntry) => Promise<GrantAnswer>;
let ask: Ask | undefined;

/** The dialog that asks; mounted once by the Shell. */
export function setGrantAsker(a: Ask | undefined) {
  ask = a;
}

/** Sign and store one grant per option, on the Node that runs the Service. */
export async function signGrants(t: Target, need: GrantsNeeded) {
  // A grant revoked or given before is replaced; sign later than it.
  let existing: Rec[] = [];
  try {
    existing = (await createClient(NodeService, transportFor(t)).listRecords({ kind: "grant" })).records;
  } catch {
    /* the put says if it matters */
  }
  const { name } = await api.deviceInfo();
  const now = BigInt(Date.now());
  for (const [i, option] of need.options.entries()) {
    const id = need.grantIds[i];
    if (!id) throw new Error(`the Node named no grant for ${option}`);
    // In a Service of several components each option is its component's,
    // for that component's image (plan C4).
    const component = need.components[i] ?? "";
    const imageRepository = need.imageRepositories[i] || need.imageRepository;
    const body = toBinary(GrantSchema, create(GrantSchema, { nodeId: need.nodeId, service: need.service, component, option, imageRepository, grantedUnixMs: now, deviceName: name }));
    await putSigned(t, { kind: "grant", id, body }, currentRecord(existing, "grant", id));
  }
}

/** The declared grant of each option the Node asks for, or undefined when the entry did not declare them all. */
export function declaredFor(entry: CatalogEntry | undefined, need: GrantsNeeded): CatalogGrant[] | undefined {
  if (!entry?.grants?.length) return undefined;
  const out = need.options.map((o, i) => entry.grants!.find((g) => grantMatches(g, need.components[i] ?? "", o)));
  return out.every(Boolean) ? (out as CatalogGrant[]) : undefined;
}

/** A Catalog install that can be retried with some of its features off (G8). */
export interface EntryInstall<T> {
  entry: CatalogEntry;
  without: (features: string[]) => Promise<T>;
}

/**
 * Run a Service call; when the Node says it needs grants, ask, sign them
 * and run it once more. Declining throws GrantsDeclined. For a Catalog
 * install whose entry declares the options, the question uses the entry's
 * words, and "without" signs only the options still needed and installs
 * with those features off.
 */
export async function withGrants<T>(t: Target, call: () => Promise<T>, install?: EntryInstall<T>): Promise<T> {
  try {
    return await call();
  } catch (e) {
    const need = grantsNeeded(e);
    if (!need || !ask) throw e;
    const declared = declaredFor(install?.entry, need);
    const answer = declared ? await ask(need, install!.entry) : await ask(need);
    if (answer === false) throw new GrantsDeclined();
    if (answer === true || !install || !declared || answer.without.length === 0) {
      await signGrants(t, need);
      return call();
    }
    const keep = need.options.map((_, i) => !(declared[i].optional && answer.without.includes(declared[i].optional)));
    const pick = <V>(xs: V[]) => xs.filter((_, i) => keep[i]);
    const still = create(GrantsNeededSchema, { ...need, options: pick(need.options), grantIds: pick(need.grantIds), components: need.components.length ? pick(need.components) : [], imageRepositories: need.imageRepositories.length ? pick(need.imageRepositories) : [] });
    if (still.options.length) await signGrants(t, still);
    // Anything the Node still asks for, the ordinary question covers.
    return withGrants(t, () => install.without(answer.without));
  }
}

/** Revoke a grant: a signed tombstone through the given Node. */
export async function revokeGrant(t: Target, record: Rec) {
  await putSigned(t, { kind: "grant", id: record.id, deleted: true }, record);
}
