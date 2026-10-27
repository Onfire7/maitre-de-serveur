// Maître Desktop. Before a Fleet exists: the first-run screens. After: the
// Console shell (src/app/). The Rust side holds the Console's own state
// (src/state.ts); the Fleet itself is read from its Nodes.
import { useCallback, useEffect, useState } from "react";
import { MotionConfig } from "motion/react";
import { api, type ConsoleState, type DeviceInfo } from "./state";
import { Welcome, RecoveryPhrase, JoinScreen, RecoverScreen, CodeScreen, type FirstRunScreen } from "./app/FirstRun";
import { Shell as ConsoleShell } from "./app/Shell";
import { RouteProvider, routeFromUrl } from "./app/routes";
import { ThemeProvider } from "./app/theme";

export default function App() {
  const [device, setDevice] = useState<DeviceInfo | null>(null);
  const [state, setState] = useState<ConsoleState | null>(null);
  // `?firstrun=welcome|join|recover|code|phrase` shows a first-run screen (screenshot fixture).
  const fixture = new URLSearchParams(window.location.search).get("firstrun");
  const [phrase, setPhrase] = useState<string | null>(fixture === "phrase" ? "abandon ability able about above absent absorb abstract absurd abuse access accident" : null);
  const [firstRun, setFirstRun] = useState<FirstRunScreen>(fixture && fixture !== "phrase" ? (fixture as FirstRunScreen) : "welcome");
  const [forceFirst, setForceFirst] = useState(!!fixture && fixture !== "phrase");

  useEffect(() => {
    api.deviceInfo().then(setDevice);
    api.getState().then(setState);
  }, []);

  const save = useCallback(async (next: ConsoleState) => {
    await api.setState(next);
    setState(next);
  }, []);

  if (!device || !state) return <div className="flex h-screen items-center justify-center text-sm text-muted-foreground">loading…</div>;

  let body: React.ReactNode;
  if (!state.fleet || forceFirst) {
    const joined = async (s: ConsoleState) => {
      await save(s);
      setFirstRun("welcome");
      setForceFirst(false);
    };
    body =
      firstRun === "welcome" ? (
        <Welcome
          device={device}
          onCreated={async (p) => {
            setState(await api.getState());
            setPhrase(p);
          }}
          onGo={setFirstRun}
        />
      ) : firstRun === "join" ? (
        <JoinScreen device={device} onJoined={joined} onCancel={() => setFirstRun("welcome")} />
      ) : firstRun === "recover" ? (
        <RecoverScreen device={device} onJoined={joined} onCancel={() => setFirstRun("welcome")} />
      ) : (
        <CodeScreen device={device} onJoined={joined} onCancel={() => setFirstRun("welcome")} />
      );
  } else if (phrase !== null) {
    body = <RecoveryPhrase phrase={phrase} onDone={() => setPhrase(null)} />;
  } else {
    body = (
      <RouteProvider initial={routeFromUrl({ name: "fleet", tab: "overview" })}>
        <ConsoleShell device={device} state={state} save={save} />
      </RouteProvider>
    );
  }
  return (
    <MotionConfig reducedMotion="user">
      <ThemeProvider>{body}</ThemeProvider>
    </MotionConfig>
  );
}
