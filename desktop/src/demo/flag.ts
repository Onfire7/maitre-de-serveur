// Demo Fleet (decision 153): a scripted Fleet behind a fake transport so the
// Console can be built, tested and screenshotted without a Node. On when
// the build sets VITE_DEMO=1 (`pnpm dev:demo`), the page is opened with
// ?demo, or localStorage has maitre.demo=1 (toggled from Preferences).
export function isDemo(): boolean {
  if (import.meta.env.VITE_DEMO === "1") return true;
  if (typeof window === "undefined") return false;
  if (new URLSearchParams(window.location.search).has("demo")) return true;
  try {
    return window.localStorage.getItem("maitre.demo") === "1";
  } catch {
    return false;
  }
}
