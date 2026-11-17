import type { UpdateStatus } from "@/gen/maitre/v1/node_pb";

/**
 * How long a Node's updates usually take, in words, from the figures the
 * Node itself reports in GetUpdateStatus (the medians of its own last
 * updated runs; 0 is no figure). The Console only words them: whole
 * minutes, "under a minute" below one, "a few minutes" without a figure.
 * With reboot false the update stages and waits, so the Services keep
 * running.
 */
export function updateTimesLine(st: Pick<UpdateStatus, "typicalUpdateMs" | "typicalDowntimeMs">, reboot = true): string {
  const total = minutes(st.typicalUpdateMs);
  const head = `Updates usually take ${total === null ? "a few minutes" : total === 0 ? "under a minute" : `about ${total} ${total === 1 ? "minute" : "minutes"}`}`;
  if (!reboot) return `${head}; its Services keep running until it next restarts.`;
  const down = minutes(st.typicalDowntimeMs);
  // "about 6 minutes; ... about 2": the unit carries over from the first half.
  const tail = down === null ? "a few minutes" : down === 0 ? "under a minute" : total ? `about ${down}` : `about ${down} ${down === 1 ? "minute" : "minutes"}`;
  return `${head}; its Services are down for ${tail}.`;
}

/** Whole minutes; 0 below one; null when the Node has no figure. */
function minutes(ms: bigint): number | null {
  if (!ms) return null;
  const n = Number(ms);
  return n < 60_000 ? 0 : Math.round(n / 60_000);
}
