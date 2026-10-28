// Where a Service's own folders live in a Space (decision 205), as on the
// Node (server/internal/layout): out of the way of the user's folders,
// under .Services, which Files hides like any name starting with a dot.
export const SERVICES_DIR = ".Services";

/** A Service's own folder for a volume, relative to its Space. */
export const volumeDir = (service: string, volume: string) => `${SERVICES_DIR}/${service}/${volume}`;

/** Names starting with a dot are hidden in Files unless asked for. */
export const hidden = (name: string) => name.startsWith(".");
