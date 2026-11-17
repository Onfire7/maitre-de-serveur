package terminal

import (
	"context"
	"maps"
	"time"

	"google.golang.org/protobuf/proto"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/store"
)

// KindTerminal is the Fleet Record kind of the Node's record of a Terminal
// (maitrev1.TerminalRecord, ADR 0011): who opened it and who attached,
// where, with what argv and user, when it started and ended, and its exit
// code. Never keystrokes or output. The Node that held the Terminal owns
// and signs it, so it reaches the Fleet's other Nodes and the Console like
// any Record.
const KindTerminal = "terminal"

// deviceName is the name the Node knows a Trusted Device by, or "" when
// it has none (the record keeps the fingerprint beside it, and the list
// shows the fingerprint instead).
func (h *Handler) deviceName(ctx context.Context, fp string) string {
	var d maitrev1.TrustedDevice
	if _, err := h.cfg.Store.Get(ctx, store.KindTrustedDevice, fp, &d); err == nil && d.GetName() != "" {
		return d.GetName()
	}
	return ""
}

// visit is a Device opening or attaching now.
func (h *Handler) visit(ctx context.Context, fp string) *maitrev1.TerminalVisit {
	return &maitrev1.TerminalVisit{Device: fp, DeviceName: h.deviceName(ctx, fp), UnixMs: time.Now().UnixMilli()}
}

// recordOpened records a Terminal just opened by the visit's Device.
//
// TerminalRecord repeats most of TerminalInfo's fields, copied here, rather
// than holding a TerminalInfo: the two differ in what they are (a Record
// every Node keeps and signs, and the holder's live answer, with running
// and opened_by for the list), a shared sub-message would renumber fields
// both already carry on the wire, and this is the one place they meet.
func (h *Handler) recordOpened(ctx context.Context, info *maitrev1.TerminalInfo, opened *maitrev1.TerminalVisit) {
	h.recording.Lock()
	defer h.recording.Unlock()
	h.putRecord(ctx, &maitrev1.TerminalRecord{
		Id: info.GetId(), Service: info.GetService(), Container: info.GetContainer(), User: info.GetUser(),
		Argv: info.GetArgv(), Root: info.GetRoot(), Opened: opened, StartedUnixMs: info.GetStartedUnixMs(),
	})
}

// recordAttach adds an attach to Terminal id's record.
func (h *Handler) recordAttach(ctx context.Context, id string, v *maitrev1.TerminalVisit) {
	h.update(ctx, id, func(r *maitrev1.TerminalRecord) bool {
		r.Attaches = append(r.Attaches, v)
		return true
	})
}

// recordEnded records how Terminal id ended, once.
func (h *Handler) recordEnded(ctx context.Context, id string, code int32, endedMs int64) {
	h.update(ctx, id, func(r *maitrev1.TerminalRecord) bool {
		if r.GetEndedUnixMs() != 0 {
			return false
		}
		if endedMs == 0 {
			endedMs = time.Now().UnixMilli()
		}
		r.EndedUnixMs, r.Code = endedMs, code
		return true
	})
}

// update changes Terminal id's record, if the Node has one, when change
// says it changed it.
func (h *Handler) update(ctx context.Context, id string, change func(*maitrev1.TerminalRecord) bool) {
	h.recording.Lock()
	defer h.recording.Unlock()
	var r maitrev1.TerminalRecord
	if _, err := h.cfg.Store.Get(ctx, KindTerminal, id, &r); err != nil {
		return
	}
	if change(&r) {
		h.putRecord(ctx, &r)
	}
}

// putRecord stores r; h.recording is held. A record that cannot be written
// is logged: the Terminal still serves.
func (h *Handler) putRecord(ctx context.Context, r *maitrev1.TerminalRecord) {
	if _, err := h.cfg.Store.Put(context.WithoutCancel(ctx), KindTerminal, r.GetId(), r, false); err != nil {
		h.cfg.Log.Warn("terminal record not written", "id", r.GetId(), "err", err)
		return
	}
	if h.unendedIDs != nil {
		if r.GetEndedUnixMs() == 0 {
			h.unendedIDs[r.GetId()] = true
		} else {
			delete(h.unendedIDs, r.GetId())
		}
	}
}

// KeepRecords is how long the Node keeps its record of a Terminal after it
// ended; a running Terminal's record is kept however old.
const KeepRecords = 30 * 24 * time.Hour

// rescanEvery is how often the records are read in full: to let go of
// those older than KeepRecords and to find again the Terminals this Node
// has not seen end. In between, the Handler keeps that set as it writes
// the records, the only writer of this Node's own.
const rescanEvery = time.Hour

// unended is this Node's records of Terminals it has not seen end, by id.
func (h *Handler) unended(ctx context.Context) map[string]bool {
	h.recording.Lock()
	defer h.recording.Unlock()
	if h.unendedIDs == nil || time.Since(h.scanned) > rescanEvery {
		h.rescan(ctx)
	}
	return maps.Clone(h.unendedIDs)
}

// rescan reads the records, deletes this Node's of Terminals that ended
// more than KeepRecords ago, and rebuilds the set of those not seen to
// end; h.recording is held.
func (h *Handler) rescan(ctx context.Context) {
	recs, err := h.cfg.Store.List(ctx, KindTerminal)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-KeepRecords).UnixMilli()
	unended := map[string]bool{}
	for _, rec := range recs {
		var r maitrev1.TerminalRecord
		if rec.GetDeleted() || rec.GetOwnerNodeId() != h.cfg.Store.NodeID() || proto.Unmarshal(rec.GetBody(), &r) != nil {
			continue
		}
		switch ended := r.GetEndedUnixMs(); {
		case ended == 0:
			unended[rec.GetId()] = true
		case ended < cutoff:
			if _, err := h.cfg.Store.Delete(context.WithoutCancel(ctx), KindTerminal, rec.GetId(), false); err != nil {
				h.cfg.Log.Warn("terminal record not deleted", "id", rec.GetId(), "err", err)
			}
		}
	}
	h.unendedIDs, h.scanned = unended, time.Now()
}

// reconcile brings the records up to date with the holders found: a
// finished one's end is recorded, and a record whose holder is gone
// without its end seen (the Node restarted) is marked lost. unended was
// read before the holders were looked for, so a Terminal opened since is
// not among them.
func (h *Handler) reconcile(ctx context.Context, unended map[string]bool, found map[string]*maitrev1.TerminalInfo, alive map[string]bool) {
	for id := range unended {
		if info := found[id]; info != nil {
			if !info.GetRunning() {
				h.recordEnded(ctx, id, info.GetCode(), info.GetEndedUnixMs())
			}
			continue
		}
		if alive[id] {
			// Its holder did not answer this time; it may still.
			continue
		}
		h.update(ctx, id, func(r *maitrev1.TerminalRecord) bool {
			if r.GetEndedUnixMs() != 0 {
				return false
			}
			r.EndedUnixMs, r.Lost = time.Now().UnixMilli(), true
			return true
		})
	}
}
