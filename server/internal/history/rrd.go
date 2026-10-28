// Package history keeps a Node's metrics for the long term (decision 210):
// one fixed-size round-robin file per series, at four levels of detail,
// each point the average and the highest of the readings in its step. The
// files never grow. The finest level lives in memory and is written once a
// minute and on stop; the coarser ones are written as each step ends.
package history

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
)

// Level is one level of detail: a step and how many steps are kept.
type Level struct {
	Step int64 // milliseconds
	Rows int
}

// Levels: 10 s for 6 hours, 1 min for 7 days, 15 min for 90 days, 1 hour
// for 2 years.
var Levels = []Level{
	{Step: 10_000, Rows: 6 * 360},
	{Step: 60_000, Rows: 7 * 24 * 60},
	{Step: 900_000, Rows: 90 * 24 * 4},
	{Step: 3_600_000, Rows: 2 * 365 * 24},
}

const (
	magic      = "MTRRD1\x00\x00"
	headerSize = 256
	levelHead  = 40 // step u32 (s), rows u32, last i64, acc start i64, acc sum f64, acc n u32, acc max f32
	slotSize   = 8  // avg f32, max f32
)

var le = binary.LittleEndian

// acc gathers the readings of the step in progress.
type acc struct {
	start int64
	sum   float64
	n     uint32
	max   float32
}

type level struct {
	Level
	off  int64 // where its slots start in the file
	last int64 // start of the newest written step; 0 for none
	acc  acc
	ring []float32 // level 0 only: its slots, kept in memory
}

// series is one open file.
type series struct {
	f      *os.File
	levels []*level
	dirty  bool
}

func layout() []*level {
	off := int64(headerSize)
	var ls []*level
	for _, l := range Levels {
		ls = append(ls, &level{Level: l, off: off})
		off += int64(l.Rows) * slotSize
	}
	return ls
}

// fileSize is the size of every series file.
func fileSize() int64 {
	ls := layout()
	last := ls[len(ls)-1]
	return last.off + int64(last.Rows)*slotSize
}

// create makes a new series file of empty (NaN) slots.
func create(path string) (*series, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	s := &series{f: f, levels: layout()}
	s.levels[0].ring = nanSlots(2 * s.levels[0].Rows)
	buf := make([]byte, fileSize())
	s.putHeader(buf[:headerSize])
	for i, v := range nanSlots(int(fileSize()-headerSize) / 4) {
		le.PutUint32(buf[headerSize+4*i:], math.Float32bits(v))
	}
	if _, err := f.WriteAt(buf, 0); err != nil {
		f.Close()
		os.Remove(path)
		return nil, err
	}
	return s, nil
}

// open reads an existing series file.
func open(path string) (*series, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	s, err := load(f)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return s, nil
}

var errFormat = errors.New("not a metrics history file of this version")

func load(f *os.File) (*series, error) {
	if st, err := f.Stat(); err != nil || st.Size() != fileSize() {
		return nil, errFormat
	}
	head := make([]byte, headerSize)
	if _, err := f.ReadAt(head, 0); err != nil {
		return nil, err
	}
	if string(head[:8]) != magic || int(le.Uint32(head[8:])) != len(Levels) {
		return nil, errFormat
	}
	s := &series{f: f, levels: layout()}
	for i, l := range s.levels {
		h := head[16+i*levelHead:]
		if int64(le.Uint32(h))*1000 != l.Step || int(le.Uint32(h[4:])) != l.Rows {
			return nil, errFormat
		}
		l.last = int64(le.Uint64(h[8:]))
		l.acc = acc{start: int64(le.Uint64(h[16:])), sum: math.Float64frombits(le.Uint64(h[24:])), n: le.Uint32(h[32:]), max: math.Float32frombits(le.Uint32(h[36:]))}
	}
	l0 := s.levels[0]
	b := make([]byte, l0.Rows*slotSize)
	if _, err := f.ReadAt(b, l0.off); err != nil {
		return nil, err
	}
	l0.ring = make([]float32, 2*l0.Rows)
	for i := range l0.ring {
		l0.ring[i] = math.Float32frombits(le.Uint32(b[4*i:]))
	}
	return s, nil
}

func (s *series) putHeader(b []byte) {
	copy(b, magic)
	le.PutUint32(b[8:], uint32(len(s.levels)))
	for i, l := range s.levels {
		h := b[16+i*levelHead:]
		le.PutUint32(h, uint32(l.Step/1000))
		le.PutUint32(h[4:], uint32(l.Rows))
		le.PutUint64(h[8:], uint64(l.last))
		le.PutUint64(h[16:], uint64(l.acc.start))
		le.PutUint64(h[24:], math.Float64bits(l.acc.sum))
		le.PutUint32(h[32:], l.acc.n)
		le.PutUint32(h[36:], math.Float32bits(l.acc.max))
	}
}

// add takes a reading at t (unix ms). A reading older than the step in
// progress is dropped (the clock went back).
func (s *series) add(t int64, v float64) error {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return nil
	}
	s.dirty = true
	for _, l := range s.levels {
		start := t - t%l.Step
		switch {
		case l.acc.n > 0 && start == l.acc.start:
		case l.acc.n > 0 && start < l.acc.start:
			continue
		default:
			if l.acc.n > 0 {
				if err := s.write(l, l.acc.start, float32(l.acc.sum/float64(l.acc.n)), l.acc.max); err != nil {
					return err
				}
			}
			l.acc = acc{start: start, max: float32(v)}
		}
		l.acc.sum += v
		l.acc.n++
		l.acc.max = max(l.acc.max, float32(v))
	}
	return nil
}

// write stores the step starting at start, marking the steps skipped since
// the last one as gaps.
func (s *series) write(l *level, start int64, avg, hi float32) error {
	if l.last != 0 && start <= l.last {
		return nil
	}
	from := start
	if l.last != 0 {
		from = max(l.last+l.Step, start-int64(l.Rows-1)*l.Step)
	}
	nan := float32(math.NaN())
	for t := from; t < start; t += l.Step {
		if err := s.put(l, t, nan, nan); err != nil {
			return err
		}
	}
	if err := s.put(l, start, avg, hi); err != nil {
		return err
	}
	l.last = start
	return nil
}

func (s *series) put(l *level, t int64, avg, hi float32) error {
	i := int((t / l.Step) % int64(l.Rows))
	if l.ring != nil {
		l.ring[2*i], l.ring[2*i+1] = avg, hi
		return nil
	}
	var b [slotSize]byte
	le.PutUint32(b[:], math.Float32bits(avg))
	le.PutUint32(b[4:], math.Float32bits(hi))
	_, err := s.f.WriteAt(b[:], l.off+int64(i)*slotSize)
	return err
}

// read returns the steps of level l covering [from, to): the start of the
// first, and avg and max for each. The step in progress is included.
func (s *series) read(l *level, from, to int64) (start int64, avg, hi []float64, err error) {
	start = from - from%l.Step
	n := int((to - start + l.Step - 1) / l.Step)
	if n <= 0 {
		return start, nil, nil, nil
	}
	avg, hi = make([]float64, n), make([]float64, n)
	var disk []byte
	if l.ring == nil {
		disk = make([]byte, l.Rows*slotSize)
		if _, err := s.f.ReadAt(disk, l.off); err != nil && err != io.EOF {
			return 0, nil, nil, err
		}
	}
	oldest := l.last - int64(l.Rows-1)*l.Step
	for k := range n {
		t := start + int64(k)*l.Step
		avg[k], hi[k] = math.NaN(), math.NaN()
		switch {
		case l.acc.n > 0 && t == l.acc.start:
			avg[k], hi[k] = l.acc.sum/float64(l.acc.n), float64(l.acc.max)
		case l.last != 0 && t <= l.last && t >= oldest:
			i := int((t / l.Step) % int64(l.Rows))
			if l.ring != nil {
				avg[k], hi[k] = float64(l.ring[2*i]), float64(l.ring[2*i+1])
			} else {
				avg[k] = float64(math.Float32frombits(le.Uint32(disk[i*slotSize:])))
				hi[k] = float64(math.Float32frombits(le.Uint32(disk[i*slotSize+4:])))
			}
		}
	}
	return start, avg, hi, nil
}

// flush writes the header and the finest level.
func (s *series) flush() error {
	if !s.dirty {
		return nil
	}
	head := make([]byte, headerSize)
	s.putHeader(head)
	if _, err := s.f.WriteAt(head, 0); err != nil {
		return err
	}
	l0 := s.levels[0]
	b := make([]byte, len(l0.ring)*4)
	for i, v := range l0.ring {
		le.PutUint32(b[4*i:], math.Float32bits(v))
	}
	if _, err := s.f.WriteAt(b, l0.off); err != nil {
		return err
	}
	s.dirty = false
	return nil
}

func nanSlots(n int) []float32 {
	out := make([]float32, n)
	nan := float32(math.NaN())
	for i := range out {
		out[i] = nan
	}
	return out
}
