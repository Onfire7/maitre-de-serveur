// Package carry carries a byte stream from one Node to another as a
// full-duplex HTTP/2 request over the Nodes' own mutual-TLS link: the
// request body is one direction, the response body the other. The Fleet's
// Service traffic between Nodes travels this way (ADR 0010, fleetnet), and
// so does a Device's connection relayed by the anchor (ADR 0012, relay).
package carry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
)

// Stream is a carried connection: writes go up the request body, reads
// come down the response body.
type Stream struct {
	w   *io.PipeWriter
	res *http.Response
}

func (s *Stream) Read(b []byte) (int, error)  { return s.res.Body.Read(b) }
func (s *Stream) Write(b []byte) (int, error) { return s.w.Write(b) }

// CloseWrite ends the direction this side sends.
func (s *Stream) CloseWrite() error { return s.w.Close() }

func (s *Stream) Close() error {
	_ = s.w.Close()
	return s.res.Body.Close()
}

// Dial opens a carried stream to path at a peer, trying its addresses in
// turn. An address that does not answer is passed over; a peer that
// answers with anything but 200 ends it with that answer.
func Dial(ctx context.Context, client *http.Client, addrs []string, path string, query url.Values) (*Stream, error) {
	if client == nil {
		return nil, errors.New("no peer link")
	}
	if len(addrs) == 0 {
		return nil, errors.New("no address")
	}
	var last error
	for _, addr := range addrs {
		pr, pw := io.Pipe()
		u := url.URL{Scheme: "https", Host: addr, Path: path, RawQuery: query.Encode()}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), pr)
		if err != nil {
			return nil, err
		}
		res, err := client.Do(req)
		if err != nil {
			_ = pw.Close()
			last = err
			continue
		}
		if res.StatusCode != http.StatusOK {
			msg, _ := io.ReadAll(io.LimitReader(res.Body, 1024))
			_ = res.Body.Close()
			_ = pw.Close()
			return nil, fmt.Errorf("%s: %s", res.Status, strings.TrimSpace(string(msg)))
		}
		return &Stream{w: pw, res: res}, nil
	}
	return nil, last
}

// answer is the serving side of a carried stream.
type answer struct {
	r  *http.Request
	w  http.ResponseWriter
	rc *http.ResponseController
}

func (a *answer) Read(b []byte) (int, error) { return a.r.Body.Read(b) }

// Close ends the request body, so a copy waiting on it stops.
func (a *answer) Close() error { return a.r.Body.Close() }
func (a *answer) Write(b []byte) (int, error) {
	n, err := a.w.Write(b)
	if err == nil {
		err = a.rc.Flush()
	}
	return n, err
}

// Answer accepts a carried stream on the serving side: it answers 200 at
// once, full duplex, and returns the stream, whose reads are the request
// body and whose writes go out flushed in the response.
func Answer(w http.ResponseWriter, r *http.Request) io.ReadWriter {
	rc := http.NewResponseController(w)
	_ = rc.EnableFullDuplex()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	_ = rc.Flush()
	return &answer{r: r, w: w, rc: rc}
}

// Join copies both ways between a and b until either direction ends or
// ctx is done, then closes both (those that are io.Closers) and waits for
// both copies to stop, so neither is touched after Join returns. It
// reports the bytes carried each way: a to b, then b to a.
func Join(ctx context.Context, a, b io.ReadWriter) (aToB, bToA int64) {
	var ab, ba atomic.Int64
	done := make(chan struct{}, 2)
	go func() { copyCounted(b, a, &ab); done <- struct{}{} }()
	go func() { copyCounted(a, b, &ba); done <- struct{}{} }()
	left := 2
	select {
	case <-done:
		left--
	case <-ctx.Done():
	}
	for _, s := range []io.ReadWriter{a, b} {
		if c, ok := s.(io.Closer); ok {
			_ = c.Close()
		}
	}
	for ; left > 0; left-- {
		<-done
	}
	return ab.Load(), ba.Load()
}

// copyCounted copies with a fixed buffer, so every read goes out as soon
// as it is read, and counts what it wrote.
func copyCounted(dst io.Writer, src io.Reader, n *atomic.Int64) {
	buf := make([]byte, 32*1024)
	for {
		r, err := src.Read(buf)
		if r > 0 {
			w, werr := dst.Write(buf[:r])
			n.Add(int64(w))
			if werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}
