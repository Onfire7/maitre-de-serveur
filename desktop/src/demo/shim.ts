// When the demo runs in a plain browser (pnpm dev:demo) there is no Tauri
// runtime. Panels that still call `invoke` directly (media, netboot, join,
// update) get a stub that rejects with a readable message instead of a
// TypeError, and `listen` gets a no-op.
import { demoApi } from "./api";

export function installTauriShim() {
  if (typeof window === "undefined" || "__TAURI_INTERNALS__" in window) return;
  let next = 1;
  const w = window as unknown as { __TAURI_INTERNALS__: unknown; __TAURI_EVENT_PLUGIN_INTERNALS__: unknown };
  w.__TAURI_EVENT_PLUGIN_INTERNALS__ = { unregisterListener: () => {} };
  w.__TAURI_INTERNALS__ = {
    invoke: async (cmd: string, args?: Record<string, unknown>) => {
      switch (cmd) {
        case "plugin:event|listen":
          return next++;
        case "plugin:event|unlisten":
          return;
        case "discover":
          return demoApi.discover();
        case "get_state":
          return demoApi.getState();
        case "device_info":
          return demoApi.deviceInfo();
        case "peek_installer":
          return demoApi.peekInstaller(String(args?.addr ?? ""));
      }
      throw new Error(`${cmd} is not available in the demo Fleet`);
    },
    transformCallback: () => next++,
    unregisterCallback: () => {},
    metadata: { currentWindow: { label: "main" }, currentWebview: { label: "main", windowLabel: "main" } },
    convertFileSrc: (p: string) => p,
  };
}
