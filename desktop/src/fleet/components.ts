// A Service's components (plan C1, docs/services-from-compose.md): a
// Service of several containers lists them in spec.components; one of a
// single container keeps image, previous_image and quadlet on the spec
// itself, and is shown here as one component with an empty name.
import type { ComponentStatus, ServiceSpec, ServiceStatus } from "@/gen/maitre/v1/service_pb";

export interface Component {
  /** Empty for a single-container Service. */
  name: string;
  image: string;
  previousImage: string;
  quadlet: string;
}

/** The Node feature that says it runs Services of several components (C5). */
export const COMPONENTS_FEATURE = "components";

export function isMulti(spec: ServiceSpec): boolean {
  return spec.components.length > 0;
}

export function componentsOf(spec: ServiceSpec): Component[] {
  if (!isMulti(spec)) return [{ name: "", image: spec.image, previousImage: spec.previousImage, quadlet: spec.quadlet }];
  return spec.components.map((c) => ({ name: c.name, image: c.image, previousImage: c.previousImage, quadlet: c.quadlet }));
}

/** Whether Roll back has something to go back to, in any component. */
export function canRollBack(spec: ServiceSpec): boolean {
  return componentsOf(spec).some((c) => !!c.previousImage);
}

/** The status the Node reported for one component, if it did. */
export function componentStatus(status: ServiceStatus | undefined, name: string): ComponentStatus | undefined {
  return status?.components.find((c) => c.name === name);
}

/** The Logs tab's source for a Service, or one of its components. */
export function logSource(service: string, component = ""): string {
  return component ? `service:${service}/${component}` : `service:${service}`;
}
