// The Fleet's time zone (decision 282): the kind "fleet_settings" record,
// id "fleet", signed by a Trusted Device. Every Node sets its clock to it
// and a Service whose TZ setting is "auto" runs in it; each Node gossips
// the zone it is on, so the Console can tell which have taken it.
import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import { FleetSettingsSchema, type Record as Rec } from "@/gen/maitre/v1/fleet_pb";
import type { FleetSnapshot } from "./useFleet";
import { currentRecord, putSigned } from "./sign";

export const FLEET_SETTINGS_KIND = "fleet_settings";
export const FLEET_SETTINGS_ID = "fleet";

/** The Fleet's time zone, or "" when none is set. */
export function fleetTimeZone(records: Rec[] | undefined): string {
  const r = currentRecord(records, FLEET_SETTINGS_KIND, FLEET_SETTINGS_ID);
  return r && !r.deleted ? fromBinary(FleetSettingsSchema, r.body).timeZone : "";
}

/** The zone this computer is on, as the browser reports it; "" when unknown. */
export function consoleTimeZone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone ?? "";
  } catch {
    return "";
  }
}

/** Every zone the browser knows, for picking; empty where the browser cannot list them. */
export function knownTimeZones(): string[] {
  try {
    const i = Intl as unknown as { supportedValuesOf?: (k: string) => string[] };
    return i.supportedValuesOf ? i.supportedValuesOf("timeZone") : [];
  } catch {
    return [];
  }
}

/** A plausible IANA zone name: Area/City, or UTC; no spaces, no paths. */
export function looksLikeTimeZone(tz: string): boolean {
  return /^[A-Za-z][A-Za-z0-9_+-]*(\/[A-Za-z0-9_+-]+){0,2}$/.test(tz) && tz !== "Local";
}

export type ZoneState = { nodeId: string; name: string; zone: string; state: "on" | "other" | "unknown" | "unreachable" };

/**
 * Where each Node stands on the Fleet's zone: on it, on another zone (not
 * applied yet, or a Node with a Maître Server older than the record), with
 * no zone reported (an older Server), or unreachable.
 */
export function nodeZones(snap: FleetSnapshot | undefined, zone: string): ZoneState[] {
  return (snap?.nodes ?? []).map((n) => {
    const st = snap?.status.get(n.nodeId);
    const z = st?.timeZone ?? "";
    const state: ZoneState["state"] = !st || !st.reachable ? "unreachable" : !z ? "unknown" : z === zone ? "on" : "other";
    return { nodeId: n.nodeId, name: n.name, zone: z, state };
  });
}

/** Write the Fleet's zone through the anchor; "" clears it. */
export async function setFleetTimeZone(snap: FleetSnapshot, zone: string) {
  const prev = currentRecord(snap.records, FLEET_SETTINGS_KIND, FLEET_SETTINGS_ID);
  const body = toBinary(FleetSettingsSchema, create(FleetSettingsSchema, { timeZone: zone }));
  await putSigned({ addr: snap.anchor.addr, fingerprint: snap.anchor.nodeId }, { kind: FLEET_SETTINGS_KIND, id: FLEET_SETTINGS_ID, body, deleted: zone === "" }, prev);
}
