package history

import (
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
)

// Keep is how long a series nobody records any more (a removed Service or
// Drive) stays, so a Service put back soon finds its graphs.
const Keep = 30 * 24 * time.Hour

// DefaultPoints is what a query without a point count gets.
const DefaultPoints = 300

const ext = ".rrd"

// Store is a Node's series, one file each under dir: node/cpu.rrd,
// service/jellyfin/memory.rrd.
type Store struct {
	dir    string
	mu     sync.Mutex
	series map[string]*series
	now    func() time.Time
}

// segment is one part of a series name.
var segment = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$`)

// ValidName reports whether name can be a series: slash-separated parts of
// letters, digits, dot, dash and underscore.
func ValidName(name string) bool {
	parts := strings.Split(name, "/")
	if len(parts) < 2 {
		return false
	}
	for _, p := range parts {
		if !segment.MatchString(p) {
			return false
		}
	}
	return true
}

// Open opens the series under dir, making it if needed. A file that cannot
// be read is set aside (renamed .bad), not fatal.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &Store{dir: dir, series: map[string]*series{}, now: time.Now}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ext) {
			return err
		}
		rel, _ := filepath.Rel(dir, strings.TrimSuffix(path, ext))
		name := filepath.ToSlash(rel)
		if !ValidName(name) {
			return nil
		}
		se, err := open(path)
		if err != nil {
			_ = os.Rename(path, path+".bad")
			return nil
		}
		s.series[name] = se
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) path(name string) string {
	return filepath.Join(s.dir, filepath.FromSlash(name)+ext)
}

// Record takes one reading of each series at t. Names that are not valid
// are skipped.
func (s *Store) Record(t time.Time, readings map[string]float64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ms := t.UnixMilli()
	var errs []error
	for name, v := range readings {
		se, ok := s.series[name]
		if !ok {
			if !ValidName(name) {
				continue
			}
			p := s.path(name)
			if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
				errs = append(errs, err)
				continue
			}
			var err error
			if se, err = create(p); err != nil {
				errs = append(errs, err)
				continue
			}
			s.series[name] = se
		}
		if err := se.add(ms, v); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
		}
	}
	return errors.Join(errs...)
}

// Flush writes what is held in memory.
func (s *Store) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var errs []error
	for name, se := range s.series {
		if err := se.flush(); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
		}
	}
	return errors.Join(errs...)
}

// Close flushes and closes every file.
func (s *Store) Close() error {
	err := s.Flush()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, se := range s.series {
		se.f.Close()
	}
	s.series = map[string]*series{}
	return err
}

// Sweep removes series with no reading for Keep.
func (s *Store) Sweep() {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := s.now().Add(-Keep).UnixMilli()
	for name, se := range s.series {
		if newest(se) >= cutoff {
			continue
		}
		se.f.Close()
		delete(s.series, name)
		p := s.path(name)
		_ = os.Remove(p)
		// Leave no empty service/<name> folder behind.
		_ = os.Remove(filepath.Dir(p))
	}
}

func newest(se *series) int64 {
	var t int64
	for _, l := range se.levels {
		t = max(t, l.last, l.acc.start)
	}
	return t
}

// match lists the stored series a query names: names, or prefixes ending
// in "/"; none means every node/ and drive/ series.
func (s *Store) match(names []string) []string {
	if len(names) == 0 {
		names = []string{"node/", "drive/"}
	}
	seen := map[string]bool{}
	var out []string
	for _, n := range names {
		for name := range s.series {
			if !seen[name] && (name == n || strings.HasSuffix(n, "/") && strings.HasPrefix(name, n)) {
				seen[name] = true
				out = append(out, name)
			}
		}
	}
	sort.Strings(out)
	return out
}

// Query returns the series asked for over [from, to) (unix ms; to 0 is
// now) from the finest level that still holds from, merging steps so there
// are no more than points (0 means DefaultPoints).
func (s *Store) Query(names []string, from, to int64, points int) ([]*maitrev1.MetricSeries, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UnixMilli()
	if to <= 0 || to > now {
		to = now + 1
	}
	if points <= 0 {
		points = DefaultPoints
	}
	// Nothing is kept from before the coarsest level's reach.
	last := Levels[len(Levels)-1]
	from = max(from, now-int64(last.Rows)*last.Step)
	if from >= to {
		return nil, fmt.Errorf("from must be before to")
	}
	li := len(Levels) - 1
	for i, l := range Levels {
		if from >= now-int64(l.Rows-1)*l.Step {
			li = i
			break
		}
	}
	var out []*maitrev1.MetricSeries
	for _, name := range s.match(names) {
		se := s.series[name]
		l := se.levels[li]
		start, avg, hi, err := se.read(l, from, to)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		k := (len(avg) + points - 1) / points
		start, avg, hi = merge(start, l.Step, avg, hi, k)
		out = append(out, &maitrev1.MetricSeries{Name: name, StartUnixMs: start, StepMs: l.Step * int64(k), Avg: avg, Max: hi})
	}
	return out, nil
}

// merge joins every k steps into one, aligned to the merged step so the
// points stay put as time moves on: the average of their averages (gaps
// left out) and the highest of their highs.
func merge(start, step int64, avg, hi []float64, k int) (int64, []float64, []float64) {
	if k <= 1 {
		return start, avg, hi
	}
	big := step * int64(k)
	lead := int((start % big) / step)
	first := start - int64(lead)*step
	n := (lead + len(avg) + k - 1) / k
	oa, oh := make([]float64, n), make([]float64, n)
	for j := range n {
		var sum float64
		var c int
		top := math.NaN()
		for i := j*k - lead; i < (j+1)*k-lead; i++ {
			if i < 0 || i >= len(avg) || math.IsNaN(avg[i]) {
				continue
			}
			sum += avg[i]
			c++
			if math.IsNaN(top) || hi[i] > top {
				top = hi[i]
			}
		}
		oa[j], oh[j] = math.NaN(), top
		if c > 0 {
			oa[j] = sum / float64(c)
		}
	}
	return first, oa, oh
}

// prefix is where a Service's series live.
func prefix(service string) string { return "service/" + service + "/" }

// Export returns a Service's series files, by the part of the name after
// service/<name>/.
func (s *Store) Export(service string) (map[string][]byte, error) {
	if !segment.MatchString(service) {
		return nil, fmt.Errorf("bad service name")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string][]byte{}
	for name, se := range s.series {
		rest, ok := strings.CutPrefix(name, prefix(service))
		if !ok {
			continue
		}
		if err := se.flush(); err != nil {
			return nil, err
		}
		b, err := os.ReadFile(s.path(name))
		if err != nil {
			return nil, err
		}
		out[rest] = b
	}
	return out, nil
}

// Import takes a Service's series files from another Node (it moved here),
// replacing any this Node started for it since.
func (s *Store) Import(service string, files map[string][]byte) error {
	if !segment.MatchString(service) {
		return fmt.Errorf("bad service name")
	}
	for rest, b := range files {
		if !segment.MatchString(rest) || int64(len(b)) != fileSize() || string(b[:8]) != magic {
			return fmt.Errorf("%s: %w", rest, errFormat)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for rest, b := range files {
		name := prefix(service) + rest
		p := s.path(name)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return err
		}
		tmp := p + ".import"
		if err := os.WriteFile(tmp, b, 0o600); err != nil {
			return err
		}
		se, err := open(tmp)
		if err != nil {
			os.Remove(tmp)
			return err
		}
		se.f.Close()
		if old, ok := s.series[name]; ok {
			old.f.Close()
			delete(s.series, name)
		}
		if err := os.Rename(tmp, p); err != nil {
			return err
		}
		if se, err = open(p); err != nil {
			return err
		}
		s.series[name] = se
	}
	return nil
}
