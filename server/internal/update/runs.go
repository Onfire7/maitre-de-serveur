package update

import (
	"os"
	"strings"
	"time"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
)

// Decision 214: a Node marks itself as updating in its own record before
// an update starts, and closes the update when it is back up, keeping its
// recent updates for how long they take.

// KeepRuns is how many finished updates a Node record keeps.
const KeepRuns = 20

// BootID is the kernel's id for this boot; "" where there is none.
func BootID() string {
	b, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// ByRollout is requested_by for the Fleet's own staged rollout, so an
// update nobody asked for is not left looking like one somebody did.
const ByRollout = "rollout"

// Begin marks n as updating to target from the image it runs now, and who
// asked (a Trusted Device's fingerprint, or ByRollout).
func Begin(n *maitrev1.Node, target, fromDigest, bootID, by string, now time.Time) {
	n.Updating = &maitrev1.NodeUpdate{StartedUnixMs: now.UnixMilli(), Target: target, FromDigest: fromDigest, BootId: bootID, RequestedBy: by}
}

// End closes n's update in progress, if any, as outcome; false when there
// was none.
func End(n *maitrev1.Node, outcome maitrev1.UpdateRun_Outcome, toDigest, msg string, now time.Time) bool {
	u := n.GetUpdating()
	if u == nil {
		return false
	}
	n.Updates = append(n.Updates, &maitrev1.UpdateRun{
		StartedUnixMs: u.GetStartedUnixMs(), EndedUnixMs: now.UnixMilli(), Target: u.GetTarget(),
		FromDigest: u.GetFromDigest(), ToDigest: toDigest, Outcome: outcome, Message: msg,
		RequestedBy: u.GetRequestedBy(),
	})
	if len(n.Updates) > KeepRuns {
		n.Updates = n.Updates[len(n.Updates)-KeepRuns:]
	}
	n.Updating = nil
	return true
}

// Back closes the update n was in when the agent starts: rebooted into a
// new image (updated), rebooted onto the old one after a failed boot
// (rolled back, rolledBackAt after it started), or no reboot at all (the
// agent restarted mid-update: failed). False when n was not updating.
func Back(n *maitrev1.Node, bootID, bootedDigest string, rolledBackAt time.Time, now time.Time) bool {
	u := n.GetUpdating()
	if u == nil {
		return false
	}
	switch {
	case u.GetBootId() != "" && u.GetBootId() == bootID:
		return End(n, maitrev1.UpdateRun_OUTCOME_FAILED, bootedDigest, "stopped before the reboot", now)
	case !rolledBackAt.IsZero() && rolledBackAt.UnixMilli() >= u.GetStartedUnixMs():
		return End(n, maitrev1.UpdateRun_OUTCOME_ROLLED_BACK, bootedDigest, "the new image failed its health checks; back on the previous one", now)
	}
	return End(n, maitrev1.UpdateRun_OUTCOME_UPDATED, bootedDigest, "", now)
}

// RolledBackAt is when greenboot last brought the previous image back
// (RollbackMarker's time); zero if never.
func RolledBackAt() time.Time {
	st, err := os.Stat(RollbackMarker)
	if err != nil {
		return time.Time{}
	}
	return st.ModTime()
}
