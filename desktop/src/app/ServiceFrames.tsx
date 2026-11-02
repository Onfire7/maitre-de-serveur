// The layer that holds every live Service frame (decision 286). The one
// whose Service is the page shown is visible; the others are kept, hidden,
// where they were, like a browser's other tabs.
//
// The layer sits at z-0: above the route's page, which it follows in the
// tree, and below the sidebar's flyouts (z-10), which reach over the page.
// At z-10 it painted over an open flyout, which then closed as soon as the
// pointer reached it.
import { frameLoaded, registerFrameElement, useFrames, shownFrame } from "./frames";

export function ServiceFrames() {
  const frames = useFrames().filter((f) => f.live);
  const shown = shownFrame();
  if (frames.length === 0) return null;
  return (
    <div className="pointer-events-none absolute inset-0 z-0" data-testid="service-frames">
      {frames.map((f) => (
        <iframe
          key={f.service}
          ref={(el) => registerFrameElement(f.service, el)}
          title={f.service}
          src={f.src}
          data-service-frame={f.service}
          onLoad={() => frameLoaded(f.service)}
          className={f.service === shown ? "pointer-events-auto absolute inset-0 h-full w-full border-0 bg-white" : "invisible absolute inset-0 h-full w-full border-0"}
          sandbox="allow-same-origin allow-scripts allow-forms allow-popups allow-downloads"
        />
      ))}
    </div>
  );
}
