package fleetnet

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// dnsServer answers the Fleet's names for the Node's containers and
// forwards the rest, over UDP and TCP on one address.
type dnsServer struct {
	udp     net.PacketConn
	tcp     net.Listener
	cfg     Config
	resolve func(ctx context.Context, host string) (string, bool)
	cancel  context.CancelFunc
}

const dnsTTL = 30

func serveDNS(ctx context.Context, cfg Config, resolve func(context.Context, string) (string, bool)) (*dnsServer, error) {
	addr := net.JoinHostPort(cfg.DNSAddr, "53")
	if cfg.Bind != nil {
		addr = cfg.Bind(cfg.DNSAddr, 53)
	}
	udp, err := net.ListenPacket("udp", addr)
	if err != nil {
		return nil, err
	}
	tcp, err := net.Listen("tcp", addr)
	if err != nil {
		_ = udp.Close()
		return nil, err
	}
	dctx, cancel := context.WithCancel(ctx)
	d := &dnsServer{udp: udp, tcp: tcp, cfg: cfg, resolve: resolve, cancel: cancel}
	go d.serveUDP(dctx)
	go d.serveTCP(dctx)
	return d, nil
}

func (d *dnsServer) close() {
	d.cancel()
	_ = d.udp.Close()
	_ = d.tcp.Close()
}

// Addr is where the server listens, for tests.
func (d *dnsServer) Addr() string { return d.udp.LocalAddr().String() }

func (d *dnsServer) serveUDP(ctx context.Context) {
	buf := make([]byte, 4096)
	for {
		n, from, err := d.udp.ReadFrom(buf)
		if err != nil {
			return
		}
		q := append([]byte(nil), buf[:n]...)
		go func() {
			if ans := d.answer(ctx, q, false); ans != nil {
				_, _ = d.udp.WriteTo(ans, from)
			}
		}()
	}
}

func (d *dnsServer) serveTCP(ctx context.Context) {
	for {
		c, err := d.tcp.Accept()
		if err != nil {
			return
		}
		go func() {
			defer c.Close()
			_ = c.SetDeadline(time.Now().Add(10 * time.Second))
			var hdr [2]byte
			if _, err := io.ReadFull(c, hdr[:]); err != nil {
				return
			}
			q := make([]byte, binary.BigEndian.Uint16(hdr[:]))
			if _, err := io.ReadFull(c, q); err != nil {
				return
			}
			ans := d.answer(ctx, q, true)
			if ans == nil {
				return
			}
			binary.BigEndian.PutUint16(hdr[:], uint16(len(ans)))
			_, _ = c.Write(append(hdr[:], ans...))
		}()
	}
}

// answer builds the reply to one query: the Fleet's names from the
// store, anything else from an upstream resolver.
func (d *dnsServer) answer(ctx context.Context, q []byte, tcp bool) []byte {
	var p dnsmessage.Parser
	hdr, err := p.Start(q)
	if err != nil {
		return nil
	}
	qs, err := p.AllQuestions()
	if err != nil || len(qs) != 1 {
		return d.forward(ctx, q, tcp)
	}
	question := qs[0]
	name := strings.TrimSuffix(strings.ToLower(question.Name.String()), ".")
	host, ok := strings.CutSuffix(name, ".local")
	if !ok || strings.Contains(host, ".") {
		return d.forward(ctx, q, tcp)
	}
	addr, found := d.resolve(ctx, host)
	rcode := dnsmessage.RCodeSuccess
	if !found {
		rcode = dnsmessage.RCodeNameError
	}
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: hdr.ID, Response: true, Authoritative: true, RecursionDesired: hdr.RecursionDesired, RecursionAvailable: true, RCode: rcode})
	b.EnableCompression()
	_ = b.StartQuestions()
	_ = b.Question(question)
	_ = b.StartAnswers()
	if found && question.Type == dnsmessage.TypeA {
		ip := net.ParseIP(addr).To4()
		if ip != nil {
			var a [4]byte
			copy(a[:], ip)
			_ = b.AResource(dnsmessage.ResourceHeader{Name: question.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: dnsTTL}, dnsmessage.AResource{A: a})
		}
	}
	out, err := b.Finish()
	if err != nil {
		return nil
	}
	return out
}

// forward sends the query on to the first upstream that answers.
func (d *dnsServer) forward(ctx context.Context, q []byte, tcp bool) []byte {
	for _, up := range upstreams(d.cfg) {
		dctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		ans, err := exchange(dctx, up, q, tcp)
		cancel()
		if err == nil {
			return ans
		}
	}
	return nil
}

func exchange(ctx context.Context, server string, q []byte, tcp bool) ([]byte, error) {
	var dialer net.Dialer
	if tcp {
		c, err := dialer.DialContext(ctx, "tcp", server)
		if err != nil {
			return nil, err
		}
		defer c.Close()
		if dl, ok := ctx.Deadline(); ok {
			_ = c.SetDeadline(dl)
		}
		var hdr [2]byte
		binary.BigEndian.PutUint16(hdr[:], uint16(len(q)))
		if _, err := c.Write(append(hdr[:], q...)); err != nil {
			return nil, err
		}
		if _, err := io.ReadFull(c, hdr[:]); err != nil {
			return nil, err
		}
		ans := make([]byte, binary.BigEndian.Uint16(hdr[:]))
		if _, err := io.ReadFull(c, ans); err != nil {
			return nil, err
		}
		return ans, nil
	}
	c, err := dialer.DialContext(ctx, "udp", server)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(dl)
	}
	if _, err := c.Write(q); err != nil {
		return nil, err
	}
	buf := make([]byte, 4096)
	n, err := c.Read(buf)
	if err != nil {
		return nil, err
	}
	return buf[:n], nil
}
