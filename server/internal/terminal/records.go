package terminal

import (
	"context"
	"time"

	"google.golang.org/protobuf/proto"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/store"
)

// KindRecord is the Fleet Record kind of the Node's record of a Terminal
// (maitrev1.TerminalRecord, ADR 0011): who opened it and who attached,
// where, with what argv and user, when it started and ended, and its exit
// code. Never keystrokes or output. The Node that held the Terminal owns
// and signs it, so it reaches the Fleet's other Nodes and the Console like
// any Record.
const KindRecord = "terminal"

// deviceName is the name the Node knows a Trusted Device by, or its
// fingerprint.
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
	if _, err := h.cfg.Store.Get(ctx, KindRecord, id, &r); err != nil {
		return
	}
	if change(&r) {
		h.putRecord(ctx, &r)
	}
}

// putRecord stores r; h.recording is held. A record that cannot be written
// is logged: the Terminal still serves.
func (h *Handler) putRecord(ctx context.Context, r *maitrev1.TerminalRecord) {
	if _, err := h.cfg.Store.Put(context.WithoutCancel(ctx), KindRecord, r.GetId(), r, false); err != nil {
		h.cfg.Log.Warn("terminal record not written", "id", r.GetId(), "err", err)
	}
}

// unended is this Node's records of Terminals it has not seen end, by id.
func (h *Handler) unended(ctx context.Context) map[string]bool {
	recs, err := h.cfg.Store.List(ctx, KindRecord)
	if err != nil {
		return nil
	}
	out := map[string]bool{}
	for _, rec := range recs {
		var r maitrev1.TerminalRecord
		if rec.GetDeleted() || rec.GetOwnerNodeId() != h.cfg.Store.NodeID() || proto.Unmarshal(rec.GetBody(), &r) != nil {
			continue
		}
		if r.GetEndedUnixMs() == 0 {
			out[r.GetId()] = true
		}
	}
	return out
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
