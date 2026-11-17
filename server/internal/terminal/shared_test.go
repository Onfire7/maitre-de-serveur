package terminal_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/terminal"
)

// attachedBy attaches Device who's view, of the given size, to Terminal id.
func attachedBy(t *testing.T, e env, service, id, who string, cols, rows uint32) *session {
	t.Helper()
	s, _, err := open(t, e, service, who)
	if err != nil {
		t.Fatal(err)
	}
	s.attach(id, cols, rows)
	if f := s.next(); f.GetStarted().GetId() != id {
		t.Fatalf("attach: %v", f)
	}
	return s
}

func (s *session) resize(cols, rows uint32) {
	s.send(&maitrev1.TerminalFrame{Frame: &maitrev1.TerminalFrame_Resize{Resize: &maitrev1.TerminalResize{Size: &maitrev1.TerminalSize{Cols: cols, Rows: rows}}}})
}

// sized reads output until the Node says the session is cols x rows.
func (s *session) sized(cols, rows uint32) {
	s.t.Helper()
	for s.size.GetCols() != cols || s.size.GetRows() != rows {
		if f := s.next(); f.GetOutput() == nil && f.GetResize() == nil {
			s.t.Fatalf("waiting for %dx%d, got %v (size %v)", cols, rows, f, s.size)
		}
	}
}

// Two Devices attached to one Terminal see the same output, and what
// either types reaches the same shell (ADR 0011: sessions are shared).
func TestTwoDevicesShareOneTerminal(t *testing.T) {
	e := start(t, "kuma")
	e.container(t, "kuma", "", "bash")
	laptop, id := opened(t, e, "kuma")
	phone := attachedBy(t, e, "kuma", id, "phone", 80, 24)

	laptop.typ("echo from-laptop-$((40+2))\n")
	laptop.until("from-laptop-42")
	phone.until("from-laptop-42")

	phone.typ("echo from-phone-$((6*7))\n")
	phone.until("from-phone-42")
	laptop.until("from-phone-42")
}

// The Device that last typed or resized sets the session's size, and every
// view hears it, so the others show the session at that size. Attaching
// alone does not take the size from a view already there.
func TestTheLastToTypeOrResizeSetsTheSize(t *testing.T) {
	e := start(t, "kuma")
	e.container(t, "kuma", "", "bash")
	laptop, id := opened(t, e, "kuma") // 80x24
	phone := attachedBy(t, e, "kuma", id, "phone", 50, 20)
	// The phone joins at the laptop's size.
	phone.sized(80, 24)

	// The phone types: the session takes the phone's size.
	phone.typ("stty size\n")
	phone.until("20 50")
	laptop.sized(50, 20)
	laptop.until("20 50")

	// The laptop's window is resized: the session takes it.
	laptop.resize(132, 41)
	phone.sized(132, 41)
	laptop.sized(132, 41)
	laptop.typ("stty size\n")
	laptop.until("41 132")

	// The phone types again: back to its own size, which it last said.
	phone.typ("stty s''ize\n")
	laptop.sized(50, 20)
}

// A view attaching to a Terminal no one else is viewing gives it its size.
func TestTheOnlyViewSetsTheSize(t *testing.T) {
	e := start(t, "kuma")
	e.container(t, "kuma", "", "bash")
	s, id := opened(t, e, "kuma")
	s.c.CloseNow()
	// Once the Node has seen the laptop leave.
	var again *session
	eventually(t, "the only view sets the size", func() bool {
		again = attachedBy(t, e, "kuma", id, "phone", 60, 18)
		if again.next().GetResize().GetSize().GetCols() == 60 {
			return true
		}
		again.c.CloseNow()
		return false
	})
	again.typ("stty size\n")
	again.until("18 60")
}

// A container holds several Terminals at once, listed separately, each its
// own shell.
func TestSeveralTerminalsRunInOneContainer(t *testing.T) {
	e := start(t, "kuma")
	e.container(t, "kuma", "", "bash")
	a, aID := opened(t, e, "kuma")
	b, bID := opened(t, e, "kuma")
	a.typ("echo pid-$$\n")
	b.typ("echo pid-$$\n")
	if a.pid() == b.pid() {
		t.Fatal("the two Terminals are one shell")
	}
	ts := list(t, e, "kuma")
	if len(ts) != 2 || find(ts, aID) == nil || find(ts, bID) == nil || !ts[0].GetRunning() || !ts[1].GetRunning() {
		t.Fatalf("listed %v", ts)
	}
}

// A container may hold up to 10 Terminals (ADR 0011): an 11th is refused
// with a message saying why. Another container is not affected, and Ending
// one makes room.
func TestAnEleventhTerminalInAContainerIsRefused(t *testing.T) {
	e := start(t, "kuma", "vault")
	e.container(t, "kuma", "", "sh")
	e.container(t, "vault", "", "sh")
	var ids []string
	for range terminal.MaxPerContainer {
		_, id := opened(t, e, "kuma")
		ids = append(ids, id)
	}
	s, _, err := open(t, e, "kuma", "laptop")
	if err != nil {
		t.Fatal(err)
	}
	s.start("kuma", 80, 24)
	x := s.next().GetExit()
	want := fmt.Sprintf("kuma already has %d Terminals", terminal.MaxPerContainer)
	if x.GetReason() != maitrev1.TerminalExit_REASON_FAILED || !strings.Contains(x.GetMessage(), want) || !strings.Contains(x.GetMessage(), "End one") {
		t.Fatalf("exit %v", x)
	}
	if n := len(list(t, e, "kuma")); n != terminal.MaxPerContainer {
		t.Fatalf("%d Terminals", n)
	}
	opened(t, e, "vault")

	if _, err := e.h.EndTerminal(context.Background(), &maitrev1.EndTerminalRequest{Service: "kuma", Id: ids[0]}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the Ended Terminal stops", func() bool { return !find(list(t, e, "kuma"), ids[0]).GetRunning() })
	opened(t, e, "kuma")
}
