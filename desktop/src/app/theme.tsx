// Theme follows the OS with an override in Preferences (decision 140).
import { createContext, useContext, useEffect, useMemo, useState, type ReactNode } from "react";

export type ThemePref = "system" | "light" | "dark";
const KEY = "maitre.theme";

interface ThemeCtx {
  pref: ThemePref;
  setPref: (p: ThemePref) => void;
  /** Resolved: what is on screen. */
  theme: "light" | "dark";
}

const Ctx = createContext<ThemeCtx>({ pref: "system", setPref: () => {}, theme: "light" });

function readPref(): ThemePref {
  try {
    const q = new URLSearchParams(window.location.search).get("theme");
    if (q === "light" || q === "dark") return q;
    const v = localStorage.getItem(KEY);
    return v === "light" || v === "dark" ? v : "system";
  } catch {
    return "system";
  }
}

export function ThemeProvider({ children }: { children: ReactNode }) {
  const [pref, setPrefState] = useState<ThemePref>(readPref);
  const [system, setSystem] = useState<"light" | "dark">(() => (window.matchMedia?.("(prefers-color-scheme: dark)").matches ? "dark" : "light"));
  useEffect(() => {
    const mq = window.matchMedia?.("(prefers-color-scheme: dark)");
    if (!mq) return;
    const on = () => setSystem(mq.matches ? "dark" : "light");
    mq.addEventListener("change", on);
    return () => mq.removeEventListener("change", on);
  }, []);
  const theme = pref === "system" ? system : pref;
  useEffect(() => {
    document.documentElement.classList.toggle("dark", theme === "dark");
  }, [theme]);
  const value = useMemo<ThemeCtx>(
    () => ({
      pref,
      theme,
      setPref: (p) => {
        setPrefState(p);
        try {
          localStorage.setItem(KEY, p);
        } catch {
          /* fine */
        }
      },
    }),
    [pref, theme],
  );
  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}

export function useTheme() {
  return useContext(Ctx);
}
