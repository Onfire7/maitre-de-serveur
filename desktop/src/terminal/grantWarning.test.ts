import { describe, expect, it } from "vitest";
import { create } from "@bufbuild/protobuf";
import { GrantSchema } from "@/gen/maitre/v1/fleet_pb";
import { grantWarning } from "./grantWarning";

const grant = (option: string, component = "") => create(GrantSchema, { service: "jellyfin", option, component });

describe("the warning in a Terminal into a Service with protected grants (#8)", () => {
  it("names each grant the Service holds", () => {
    expect(grantWarning([grant("Volume=/mnt/media"), grant("PodmanArgs=--privileged")])).toBe(
      "This Service has access to the Node: /mnt/media, privileged. Commands here can change the Node itself.",
    );
  });

  it("names capabilities, devices, the Node's network and other options in short", () => {
    expect(grantWarning([grant("AddCapability=SYS_ADMIN NET_ADMIN"), grant("AddDevice=/dev/dri"), grant("Network=host"), grant("[Service] ExecStartPre=/bin/true")])).toBe(
      "This Service has access to the Node: SYS_ADMIN NET_ADMIN, /dev/dri, the Node's network, [Service] ExecStartPre=/bin/true. Commands here can change the Node itself.",
    );
  });

  it("says nothing for a Service without grants", () => {
    expect(grantWarning([])).toBeUndefined();
  });

  it("names only the grants of the container the Terminal runs in, in a Service of several", () => {
    const grants = [grant("AddDevice=/dev/dri", "server"), grant("Volume=/srv/db", "database")];
    expect(grantWarning(grants, "server")).toBe("This Service has access to the Node: /dev/dri. Commands here can change the Node itself.");
    expect(grantWarning(grants, "redis")).toBeUndefined();
  });
});
