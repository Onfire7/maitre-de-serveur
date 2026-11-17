package terminal_test

import (
	"context"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/terminal"
)

// The test binary is also `maitre terminal-hold`, so a holder can be a
// process of its own, as the agent's transient unit is, and outlive the
// "agent" a test restarts.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "terminal-hold" {
		if err := terminal.HoldMain(os.Args[2:]); err != nil {
			os.Stderr.WriteString(err.Error() + "\n")
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func (s *session) attach(id string, cols, rows uint32) {
	s.send(&maitrev1.TerminalFrame{Frame: &maitrev1.TerminalFrame_Attach{Attach: &maitrev1.TerminalAttach{Id: id, Size: &maitrev1.TerminalSize{Cols: cols, Rows: rows}}}})
}

// opened opens a new Terminal into service and returns it and its id.
func opened(t *testing.T, e env, service string) (*session, string) {
	t.Helper()
	s, _, err := open(t, e, service, "laptop")
	if err != nil {
		t.Fatal(err)
	}
	s.start(service, 80, 24)
	st := s.next().GetStarted()
	if st.GetId() == "" {
		t.Fatalf("started: %v", st)
	}
	return s, st.GetId()
}

// reattached attaches a new view to Terminal id and returns it with what
// the Node said it is.
func reattached(t *testing.T, e env, service, id string) (*session, *maitrev1.TerminalStarted) {
	t.Helper()
	s, _, err := open(t, e, service, "laptop")
	if err != nil {
		t.Fatal(err)
	}
	s.attach(id, 80, 24)
	f := s.next()
	if f.GetStarted() == nil {
		t.Fatalf("attach: %v", f)
	}
	return s, f.GetStarted()
}

func list(t *testing.T, e env, service string) []*maitrev1.TerminalInfo {
	t.Helper()
	res, err := e.h.ListTerminals(context.Background(), &maitrev1.ListTerminalsRequest{Service: service})
	if err != nil {
		t.Fatal(err)
	}
	return res.GetTerminals()
}

// eventually polls cond for a while.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(15 * time.Second); !cond(); {
		if time.Now().After(deadline) {
			t.Fatalf("never: %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func find(ts []*maitrev1.TerminalInfo, id string) *maitrev1.TerminalInfo {
	for _, t := range ts {
		if t.GetId() == id {
			return t
		}
	}
	return nil
}

var pidRe = regexp.MustCompile(`pid-(\d+)`)

// pid reads output until the shell has said its pid (`echo pid-$$`).
func (s *session) pid() string {
	s.t.Helper()
	for pidRe.FindStringSubmatch(s.out.String()) == nil {
		if f := s.next(); f.GetOutput() == nil {
			s.t.Fatalf("waiting for the pid, got %v (output %q)", f, s.out.String())
		}
	}
	return pidRe.FindStringSubmatch(s.out.String())[1]
}

// Closing the view only detaches: the same process runs on, and a view
// attached again gets its recent output first.
func TestClosingTheViewLeavesTheTerminalRunningAndReattachingReplaysIt(t *testing.T) {
	e := start(t, "kuma")
	e.container(t, "kuma", "", "bash")
	s, id := opened(t, e, "kuma")
	s.typ("echo pid-$$\n")
	pid := s.pid()
	s.c.CloseNow()

	ts := list(t, e, "kuma")
	if len(ts) != 1 || ts[0].GetId() != id || !ts[0].GetRunning() || ts[0].GetContainer() != "kuma" || ts[0].GetUser() != "root" {
		t.Fatalf("listed %v", ts)
	}
	again, st := reattached(t, e, "kuma", id)
	if st.GetId() != id || st.GetContainer() != "kuma" || st.GetStartedUnixMs() == 0 {
		t.Fatalf("started %v", st)
	}
	again.until("pid-" + pid)
	again.typ("echo again-$$\n")
	again.until("again-" + pid)
}

// A Terminal that finished while no one watched keeps its exit code and
// output: the next view replays it and shows the code, and once seen it is
// gone. The list says which run and which finished.
func TestAFinishedTerminalIsShownOnceThenGone(t *testing.T) {
	e := start(t, "kuma", "vault")
	e.container(t, "kuma", "", "bash")
	e.container(t, "vault", "", "bash")
	running, runningID := opened(t, e, "kuma")
	s, id := opened(t, e, "kuma")
	s.typ("echo bye; sleep 0.3; exit 3\n")
	s.until("bye")
	s.c.CloseNow()
	other, _ := opened(t, e, "vault")
	defer other.c.CloseNow()
	defer running.c.CloseNow()

	eventually(t, "the Terminal is listed as finished", func() bool {
		f := find(list(t, e, "kuma"), id)
		return f != nil && !f.GetRunning()
	})
	ts := list(t, e, "kuma")
	if len(ts) != 2 || !find(ts, runningID).GetRunning() {
		t.Fatalf("listed %v", ts)
	}
	if f := find(ts, id); f.GetCode() != 3 || f.GetEndedUnixMs() == 0 {
		t.Fatalf("finished %v", f)
	}

	v, _ := reattached(t, e, "kuma", id)
	v.until("bye")
	if x := v.exit(); x.GetReason() != maitrev1.TerminalExit_REASON_EXITED || x.GetCode() != 3 {
		t.Fatalf("exit %v", x)
	}
	eventually(t, "the seen Terminal is gone", func() bool { return find(list(t, e, "kuma"), id) == nil })
}

// Unseen, a finished Terminal is kept for 24 hours (here, shortened).
func TestAnUnseenFinishedTerminalIsGoneAfterADay(t *testing.T) {
	defer terminal.SetKeepFinished(500 * time.Millisecond)()
	e := start(t, "kuma")
	e.container(t, "kuma", "", "bash")
	s, id := opened(t, e, "kuma")
	s.c.CloseNow()
	if err := endTerminal(e, "kuma", id); err != nil {
		t.Fatal(err)
	}
	eventually(t, "it finishes", func() bool { f := find(list(t, e, "kuma"), id); return f != nil && !f.GetRunning() })
	eventually(t, "it is forgotten", func() bool { return find(list(t, e, "kuma"), id) == nil })
}

func endTerminal(e env, service, id string) error {
	_, err := e.h.EndTerminal(context.Background(), &maitrev1.EndTerminalRequest{Service: service, Id: id})
	return err
}

// End hangs the process up; one that ignores the hangup is killed 5
// seconds later.
func TestEndHangsUpThenKillsAfterFiveSeconds(t *testing.T) {
	e := start(t, "kuma")
	e.container(t, "kuma", "", "bash")
	s, id := opened(t, e, "kuma")
	if err := endTerminal(e, "kuma", id); err != nil {
		t.Fatal(err)
	}
	if x := s.exit(); x.GetCode() != 128+1 {
		t.Fatalf("a hung-up shell: %v", x)
	}

	s, id = opened(t, e, "kuma")
	s.typ("trap '' HUP; echo de''af\n")
	s.until("deaf")
	began := time.Now()
	if err := endTerminal(e, "kuma", id); err != nil {
		t.Fatal(err)
	}
	if x := s.exit(); x.GetCode() != 128+9 {
		t.Fatalf("a shell deaf to the hangup: %v", x)
	}
	if d := time.Since(began); d < 4500*time.Millisecond || d > 12*time.Second {
		t.Fatalf("killed after %v", d)
	}
}

// Another Service's Terminal is not this one's to attach to or End, and
// an id that is no Terminal is refused.
func TestATerminalIsOnlyReachedThroughItsService(t *testing.T) {
	e := start(t, "kuma", "vault")
	e.container(t, "kuma", "", "bash")
	_, id := opened(t, e, "kuma")
	for _, bad := range []string{id, "0123456789abcdef", "../../etc/passwd"} {
		s, _, err := open(t, e, "vault", "laptop")
		if err != nil {
			t.Fatal(err)
		}
		s.attach(bad, 80, 24)
		if x := s.next().GetExit(); x.GetReason() != maitrev1.TerminalExit_REASON_FAILED || !strings.Contains(x.GetMessage(), "no longer") {
			t.Fatalf("%q: %v", bad, x)
		}
		if err := endTerminal(e, "vault", bad); err == nil {
			t.Fatalf("%q: vault ended it", bad)
		}
	}
	if f := find(list(t, e, "kuma"), id); f == nil || !f.GetRunning() {
		t.Fatalf("kuma's Terminal: %v", f)
	}
}

// Stopping the container ends its Terminals.
func TestStoppingTheContainerEndsItsTerminals(t *testing.T) {
	e := start(t, "kuma")
	e.container(t, "kuma", "", "bash")
	s, id := opened(t, e, "kuma")
	s.typ("echo up\n")
	s.until("up")
	if out, err := exec.Command(e.podman, "stop", "kuma").CombinedOutput(); err != nil {
		t.Fatalf("stop: %v %s", err, out)
	}
	if x := s.exit(); x.GetCode() != 128+9 {
		t.Fatalf("exit %v", x)
	}
	_ = id
}

// The agent restarting (an update, say) in the middle of a long command
// leaves the command running in its holder, a process of its own, and the
// agent that comes back finds it and the Console reattaches to it.
func TestAnAgentRestartLeavesTheCommandRunningAndTheConsoleReattaches(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	e := startWith(t, terminal.Process{Self: self}, "", "kuma")
	e.container(t, "kuma", "", "bash")
	s, id := opened(t, e, "kuma")
	// The holder is a process of its own: End it, then let it go, so it
	// does not outlive the test.
	t.Cleanup(func() {
		for range 50 {
			if endTerminal(e, "kuma", id) != nil {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	})
	s.typ("echo pid-$$; for i in $(seq 1 40); do echo tick-$i; sleep 0.1; done; echo done-$$\n")
	pid := s.pid()
	s.until("tick-2")

	// The agent goes away, its connections with it.
	e.srv.CloseClientConnections()
	e.srv.Close()
	back := serveAgent(t, e)

	ts := list(t, back, "kuma")
	if len(ts) != 1 || ts[0].GetId() != id || !ts[0].GetRunning() {
		t.Fatalf("the agent that came back lists %v", ts)
	}
	v, _ := reattached(t, back, "kuma", id)
	v.until("pid-" + pid)
	v.until("tick-2")
	v.until("done-" + pid)
	if !strings.Contains(v.out.String(), "tick-40") {
		t.Fatalf("output %q", v.out.String())
	}
}
