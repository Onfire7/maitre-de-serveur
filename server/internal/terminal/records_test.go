package terminal_test

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/store"
	"github.com/Onfire7/maitre-de-serveur/server/internal/terminal"
)

// device adds a Trusted Device the Node knows by name.
func (e env) device(t *testing.T, fp, name string) {
	t.Helper()
	if _, err := e.st.Put(context.Background(), store.KindTrustedDevice, fp, &maitrev1.TrustedDevice{DeviceId: fp, Name: name}, true); err != nil {
		t.Fatal(err)
	}
}

// record is the Node's record of Terminal id.
func record(t *testing.T, e env, id string) *maitrev1.Terminal {
	t.Helper()
	var r maitrev1.Terminal
	if _, err := e.st.Get(context.Background(), terminal.KindTerminal, id, &r); err != nil {
		t.Fatalf("the record of %s: %v", id, err)
	}
	return &r
}

// Opening, attaching to and ending a Terminal each leave their mark in the
// Node's record of it: who opened it, where, with what argv and user, who
// attached and when, when it ended and its exit code (ADR 0011).
func TestTheNodeRecordsWhoOpenedAttachedAndHowItEnded(t *testing.T) {
	e := start(t, "kuma")
	e.container(t, "kuma", "1000", "bash", "sh")
	e.device(t, "laptop", "Laptop")
	e.device(t, "phone", "Phone")

	s, id := opened(t, e, "kuma")
	r := record(t, e, id)
	if r.GetService() != "kuma" || r.GetContainer() != "kuma" || r.GetUser() != "1000" || r.GetRoot() ||
		len(r.GetArgv()) != 1 || r.GetArgv()[0] != "/bin/bash" {
		t.Fatalf("opened: %v", r)
	}
	if o := r.GetOpened(); o.GetDevice() != "laptop" || o.GetDeviceName() != "Laptop" || o.GetUnixMs() == 0 {
		t.Fatalf("opened by: %v", o)
	}
	if r.GetStartedUnixMs() == 0 || !r.GetRunning() || r.GetEndedUnixMs() != 0 || len(r.GetAttaches()) != 0 {
		t.Fatalf("a running Terminal's record: %v", r)
	}

	p := attachedBy(t, e, "kuma", id, "phone", 80, 24)
	r = record(t, e, id)
	if a := r.GetAttaches(); len(a) != 1 || a[0].GetDevice() != "phone" || a[0].GetDeviceName() != "Phone" || a[0].GetUnixMs() < r.GetOpened().GetUnixMs() {
		t.Fatalf("attaches: %v", r.GetAttaches())
	}

	s.typ("exit 3\n")
	if x := s.exit(); x.GetCode() != 3 {
		t.Fatalf("exit %v", x)
	}
	p.exit()
	r = record(t, e, id)
	if r.GetRunning() || r.GetEndedUnixMs() < r.GetStartedUnixMs() || r.GetCode() != 3 || r.GetLost() {
		t.Fatalf("ended: %v", r)
	}
}

// A Terminal started with Run as root says so in its info and its record,
// and the list says who opened it.
func TestTheListSaysWhoOpenedATerminalAndWhetherAsRoot(t *testing.T) {
	e := start(t, "kuma")
	e.container(t, "kuma", "1000", "bash")
	e.device(t, "laptop", "Laptop")
	s, _, err := open(t, e, "kuma", "laptop")
	if err != nil {
		t.Fatal(err)
	}
	s.startWith(&maitrev1.TerminalStart{Service: "kuma", Root: true})
	id := s.started().GetId()
	plain, plainID := opened(t, e, "kuma")
	defer plain.typ("exit\n")
	defer s.typ("exit\n")

	ts := list(t, e, "kuma")
	if i := find(ts, id); !i.GetRoot() || i.GetUser() != "root" || i.GetOpened().GetDeviceName() != "Laptop" {
		t.Fatalf("as root: %v", i)
	}
	if i := find(ts, plainID); i.GetRoot() || i.GetUser() != "1000" || i.GetOpened().GetDeviceName() != "Laptop" {
		t.Fatalf("as the image's user: %v", i)
	}
	if !record(t, e, id).GetRoot() || record(t, e, plainID).GetRoot() {
		t.Fatal("the records do not say which ran as root")
	}
}

// A Device the Node has no name for is named by its key's fingerprint.
func TestAnUnnamedDeviceIsRecordedByItsFingerprint(t *testing.T) {
	e := start(t, "kuma")
	e.container(t, "kuma", "", "bash")
	s, id := opened(t, e, "kuma")
	defer s.typ("exit\n")
	if o := record(t, e, id).GetOpened(); o.GetDevice() != "laptop" || o.GetDeviceName() != "" {
		t.Fatalf("opened by: %v", o)
	}
	if o := find(list(t, e, "kuma"), id).GetOpened(); o.GetDevice() != "laptop" || o.GetDeviceName() != "" {
		t.Fatalf("info: %v", o)
	}
}

// Ending a Terminal from the list records its end too, though no view saw it.
func TestEndingFromTheListRecordsTheEnd(t *testing.T) {
	e := start(t, "kuma")
	e.container(t, "kuma", "", "bash")
	s, id := opened(t, e, "kuma")
	s.c.CloseNow()
	if err := endTerminal(e, "kuma", id); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the end is recorded", func() bool {
		list(t, e, "kuma")
		return record(t, e, id).GetEndedUnixMs() != 0
	})
	if r := record(t, e, id); r.GetCode() != 128+1 || r.GetLost() {
		t.Fatalf("ended by SIGHUP: %v", r)
	}
	endTerminal(e, "kuma", id)
}

// A Terminal whose holder is gone before its end was seen (the Node
// restarted) is recorded as lost when the Node next looks.
func TestATerminalLostWithItsHolderIsRecordedAsLost(t *testing.T) {
	e := start(t, "kuma")
	e.container(t, "kuma", "", "bash")
	s, id := opened(t, e, "kuma")
	// The Node restarts: the holder and its socket are gone.
	s.c.CloseNow()
	if err := endTerminal(e, "kuma", id); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the holder goes", func() bool {
		if find(list(t, e, "kuma"), id) == nil {
			return true
		}
		endTerminal(e, "kuma", id)
		return false
	})
	// Its end was recorded by the list on the way; forget that, as if the
	// Node had gone down before it could.
	r := record(t, e, id)
	r.Running, r.EndedUnixMs, r.Code = true, 0, 0
	if _, err := e.st.Put(context.Background(), terminal.KindTerminal, id, r, false); err != nil {
		t.Fatal(err)
	}
	// The agent starts again.
	e = serveAgent(t, e)
	list(t, e, "kuma")
	if r := record(t, e, id); !r.GetLost() || r.GetRunning() || r.GetEndedUnixMs() == 0 {
		t.Fatalf("lost: %v", r)
	}
}

// What is typed into a Terminal and what it writes are never stored: not
// in the records, not in the Fleet database, not in any file the Node
// keeps (ADR 0011). Only the holder's memory has the output, to replay.
func TestNoKeystrokesOrOutputAreStoredAnywhere(t *testing.T) {
	e := start(t, "kuma")
	e.container(t, "kuma", "", "bash")
	e.device(t, "laptop", "Laptop")
	s, id := opened(t, e, "kuma")
	// The typed text and the output differ ('' vanishes in the shell), so
	// each can be looked for on its own.
	typed, output := "hunter''2-pass''word", "hunter2-password"
	s.typ("echo " + typed + "\n")
	s.until(output)
	v := attachedBy(t, e, "kuma", id, "laptop", 80, 24)
	v.typ("printf '%s\\n' an-ans''wer\n")
	s.until("an-answer")
	s.typ("exit 0\n")
	s.exit()
	v.exit()
	if r := record(t, e, id); r.GetEndedUnixMs() == 0 {
		t.Fatalf("not ended: %v", r)
	}
	// Every record, as stored and as it would be sent to another Node.
	recs, err := e.st.List(context.Background(), terminal.KindTerminal)
	if err != nil || len(recs) == 0 {
		t.Fatalf("records %v: %v", recs, err)
	}
	secrets := [][]byte{[]byte(typed), []byte(output), []byte("hunter"), []byte("an-ans"), []byte("an-answer")}
	for _, r := range recs {
		for _, sec := range secrets {
			if bytes.Contains(r.GetBody(), sec) {
				t.Errorf("record %s holds %q", r.GetId(), sec)
			}
		}
	}
	// Every file under the Node's state, its sockets and the containers.
	for _, dir := range []string{filepath.Dir(e.root), e.dir} {
		filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || !d.Type().IsRegular() {
				return nil
			}
			b, _ := os.ReadFile(path)
			for _, sec := range secrets {
				if bytes.Contains(b, sec) {
					t.Errorf("%s holds %q", path, sec)
				}
			}
			return nil
		})
	}
}

// The Node lets its records of finished Terminals go 30 days after they
// ended, as the Fleet database deletes (a tombstone); a running Terminal's
// record, a recent one, and another Node's are kept.
func TestRecordsOfTerminalsFinishedLongAgoAreDeleted(t *testing.T) {
	e := start(t, "kuma")
	ctx := context.Background()
	day := int64(24 * time.Hour / time.Millisecond)
	now := time.Now().UnixMilli()
	put := func(id string, r *maitrev1.Terminal) {
		t.Helper()
		r.Id = id
		if _, err := e.st.Put(ctx, terminal.KindTerminal, id, r, false); err != nil {
			t.Fatal(err)
		}
	}
	put("old", &maitrev1.Terminal{Service: "kuma", StartedUnixMs: now - 40*day, EndedUnixMs: now - 31*day})
	put("oldlost", &maitrev1.Terminal{Service: "kuma", StartedUnixMs: now - 40*day, EndedUnixMs: now - 31*day, Lost: true})
	put("recent", &maitrev1.Terminal{Service: "kuma", StartedUnixMs: now - 40*day, EndedUnixMs: now - 29*day})
	body, _ := proto.Marshal(&maitrev1.Terminal{Id: "theirs", StartedUnixMs: now - 40*day, EndedUnixMs: now - 31*day})
	if err := e.st.Seed(ctx, &maitrev1.Record{Kind: terminal.KindTerminal, Id: "theirs", OwnerNodeId: "other", Version: 1, UpdatedUnixMs: now, Body: body}); err != nil {
		t.Fatal(err)
	}
	e.container(t, "kuma", "", "bash")
	s, running := opened(t, e, "kuma")
	defer s.typ("exit\n")
	// A running Terminal opened long ago (the clock says so) stays.
	r := record(t, e, running)
	r.StartedUnixMs = now - 40*day
	put(running, r)

	list(t, e, "kuma")
	for id, gone := range map[string]bool{"old": true, "oldlost": true, "recent": false, "theirs": false, running: false} {
		raw, err := e.st.Raw(ctx, terminal.KindTerminal, id)
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if raw.GetDeleted() != gone {
			t.Fatalf("%s: deleted %v, want %v", id, raw.GetDeleted(), gone)
		}
	}
}
