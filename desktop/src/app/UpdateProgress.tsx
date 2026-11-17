import { TimedBar, type TimedSection } from "@/components/timed-bar";
import type { Job } from "@/fleet/updateQueue";

/**
 * An update's progress, while it stages ("working") or restarts ("down"):
 * one TimedBar in Downloading, Restarting and Overtime, sized from the
 * Node's figures kept on the job (staging is the typical total minus the
 * typical downtime). Downloading fills by the staging percentage the Node
 * reports, Restarting by the time since the restart began. Staged without a
 * restart: Downloading only. Without figures: the staging percentage, then
 * the pulsing bar labelled with the elapsed time.
 */
export function UpdateProgress({ job }: { job: Job }) {
  const { m, phase, typical } = job;
  const reboot = m.kind === "update" && m.reboot;
  const stagingMs = typical.updateMs - typical.downtimeMs;
  // A restarting update needs both figures; a staged one only the total.
  const figures = stagingMs > 0 && (!reboot || typical.downtimeMs > 0);
  // Staging is over once the Node restarts.
  const progress = phase === "down" ? 1 : job.progress;

  let bar;
  if (figures) {
    const sections: TimedSection[] = [{ label: "Downloading", typicalMs: stagingMs, progress }];
    if (reboot) sections.push({ label: "Restarting", typicalMs: typical.downtimeMs, since: job.restarted });
    bar = <TimedBar since={job.started} sections={sections} />;
  } else if (phase === "down") bar = <TimedBar since={job.started} sections={[{ label: "Restarting", typicalMs: 0, since: job.restarted }]} />;
  else {
    const pct = Math.round(Math.min(1, Math.max(0, progress)) * 100);
    bar = (
      <div role="progressbar" aria-label="Progress" aria-valuemin={0} aria-valuemax={100} aria-valuenow={pct} className="h-1.5 overflow-hidden rounded bg-muted">
        <div className="h-full rounded bg-status-starting transition-[width]" style={{ width: `${pct}%` }} />
      </div>
    );
  }

  return (
    <>
      {bar}
      {phase === "down" ? (
        <p className="text-sm text-muted-foreground">The Node is restarting. Its monitor shows the Maître banner when it is back.</p>
      ) : (
        <p className="text-sm text-muted-foreground">{job.message || "starting"}</p>
      )}
    </>
  );
}
