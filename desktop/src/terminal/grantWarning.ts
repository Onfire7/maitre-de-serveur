// A Terminal into a Service with protected grants (ADR 0008, decision 189)
// can do what those grants let the container do, to the Node itself. The
// Terminal says so in one line, naming them; nothing is to be confirmed,
// since the Trusted Device that signed the grants could already do as much
// (ADR 0011).
import type { Grant } from "@/gen/maitre/v1/fleet_pb";

/** A protected option in a word or two: the folder, device or capability it gives. */
function grantName(option: string): string {
  const o = option.trim();
  if (o.startsWith("[")) return o;
  const [key, ...rest] = o.split("=");
  const value = rest.join("=");
  switch (key) {
    case "Volume":
      return value.split(":")[0] || o;
    case "AddDevice":
      return value.split(":")[0] || o;
    case "AddCapability":
      return value || o;
    case "Network":
      return value === "host" ? "the host's network" : o;
    case "PodmanArgs":
      return /^--privileged(=true)?$/.test(value.trim()) ? "privileged" : o;
    case "Privileged":
    case "privileged":
      return "privileged";
    default:
      return o;
  }
}

/**
 * The warning line for a Terminal into a Service holding these grants, or
 * undefined when it holds none. In a Service of several containers,
 * `container` is the component the Terminal runs in: only its grants (and
 * any not tied to a component) are named.
 */
export function grantWarning(grants: Grant[], container?: string): string | undefined {
  const mine = grants.filter((g) => !g.component || container === undefined || g.component === container);
  if (mine.length === 0) return undefined;
  const names = [...new Set(mine.map((g) => grantName(g.option)))];
  return `This Service has host access: ${names.join(", ")}. Commands here can change the Node itself.`;
}
