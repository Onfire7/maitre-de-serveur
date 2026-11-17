package carry_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Onfire7/maitre-de-serveur/server/internal/carry"
)

// peer serves path as a Node does: it answers the carried stream and
// echoes it back, upper-cased, until the caller ends its side.
func peer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/carry", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("service") != "kuma" {
			http.Error(w, "no such Service", http.StatusNotFound)
			return
		}
		s := carry.Answer(w, r)
		buf := make([]byte, 64)
		for {
			n, err := s.Read(buf)
			if n > 0 {
				if _, werr := s.Write([]byte(strings.ToUpper(string(buf[:n])))); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	})
	srv := httptest.NewUnstartedServer(mux)
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv, strings.TrimPrefix(srv.URL, "https://")
}

func deadAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

// A carried stream is full duplex: each write is answered before the
// caller ends its side, and an address that does not answer is passed over.
func TestACarriedStreamIsFullDuplexAndTriesEachAddress(t *testing.T) {
	srv, addr := peer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := carry.Dial(ctx, srv.Client(), []string{deadAddr(t), addr}, "/carry", map[string][]string{"service": {"kuma"}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, word := range []string{"ping", "pong"} {
		if _, err := s.Write([]byte(word)); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 4)
		if _, err := io.ReadFull(s, buf); err != nil {
			t.Fatal(err)
		}
		if got := string(buf); got != strings.ToUpper(word) {
			t.Fatalf("got %q", got)
		}
	}
	s.CloseWrite()
	if rest, err := io.ReadAll(s); err != nil || len(rest) != 0 {
		t.Fatalf("after the end: %q %v", rest, err)
	}
}

// A peer that answers with anything but 200 ends the dial with its answer.
func TestAPeersRefusalIsTheAnswer(t *testing.T) {
	srv, addr := peer(t)
	_, err := carry.Dial(context.Background(), srv.Client(), []string{addr}, "/carry", map[string][]string{"service": {"ghost"}})
	if err == nil || !strings.Contains(err.Error(), "404") || !strings.Contains(err.Error(), "no such Service") {
		t.Fatalf("err %v", err)
	}
	if _, err := carry.Dial(context.Background(), srv.Client(), nil, "/carry", nil); err == nil {
		t.Fatal("no address: dialed")
	}
	if _, err := carry.Dial(context.Background(), nil, []string{addr}, "/carry", nil); err == nil {
		t.Fatal("no client: dialed")
	}
}

// Join copies both ways, closes both ends when one direction ends, and
// counts what went each way.
func TestJoinCarriesBothWaysAndClosesBoth(t *testing.T) {
	a, aPeer := net.Pipe()
	b, bPeer := net.Pipe()
	done := make(chan [2]int64)
	go func() {
		x, y := carry.Join(context.Background(), a, b)
		done <- [2]int64{x, y}
	}()
	go func() {
		buf := make([]byte, 16)
		n, _ := bPeer.Read(buf)
		bPeer.Write([]byte("reply:" + string(buf[:n])))
	}()
	aPeer.Write([]byte("hello"))
	buf := make([]byte, 16)
	n, _ := io.ReadAtLeast(aPeer, buf, len("reply:hello"))
	if got := string(buf[:n]); got != "reply:hello" {
		t.Fatalf("got %q", got)
	}
	aPeer.Close()
	select {
	case got := <-done:
		if got != [2]int64{5, 11} {
			t.Fatalf("counts %v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Join did not return when one side ended")
	}
	if _, err := bPeer.Read(buf); err == nil {
		t.Fatal("the other side was left open")
	}
}
