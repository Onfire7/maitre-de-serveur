// Package copies keeps each file of a Space on as many Drives as the Space
// asks (ADR 0009, decisions 249-253, 259, 260): the mergerfs.dup idea in
// Go. MergerFS writes a file to one branch and renames or deletes it on
// every branch, so copies drift only when a file is written. The agent
// watches each Space's folders with inotify and copies a file once it has
// been closed and left alone for a while (Watch), and runs a full pass
// over every file each hour to catch what the watches missed (Pass). A
// copy goes to a Drive on another physical disk, the roomiest first; for a
// Space that spans Nodes with "copies on different Nodes", to a peer's
// Pool when one is mounted. The newest copy by modification time is the
// source; older copies are brought up to date. Copies are checked after
// writing by reading them back against the source's hash.
package copies

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/pool"
	"github.com/Onfire7/maitre-de-serveur/server/internal/storage"
	"github.com/Onfire7/maitre-de-serveur/server/internal/store"
)

// Quiet is how long a changed file is left alone before it is copied.
const Quiet = 15 * time.Second

// Manager keeps the copies of one Node's Spaces.
type Manager struct {
	st   *store.Store
	log  *slog.Logger
	self string
	stor *storage.Manager
	// Mounts is where Drives are mounted; Remote where peers' Pools are.
	Mounts, Remote string
	Mounted        func(dir string) bool
	Free           func(dir string) uint64
	Now            func() time.Time
	// DiskOf maps a Drive id to its physical disk; from the storage
	// manager's listing by default.
	DiskOf func(ctx context.Context) map[string]string

	mu      sync.Mutex
	status  map[string]*maitrev1.CopiesStatus
	running bool
	pending map[string]time.Time // changed files by absolute path, and when they went quiet
	wake    chan struct{}
}

// New builds a Manager.
func New(st *store.Store, log *slog.Logger, self string, stor *storage.Manager) *Manager {
	m := &Manager{
		st: st, log: log, self: self, stor: stor, Mounts: pool.DrivesDir, Remote: storage.RemoteDir,
		Mounted: pool.Mounted, Free: storage.FreeBytes, Now: time.Now,
		status: map[string]*maitrev1.CopiesStatus{}, pending: map[string]time.Time{}, wake: make(chan struct{}, 1),
	}
	m.DiskOf = m.disksFromListing
	return m
}

func (m *Manager) disksFromListing(ctx context.Context) map[string]string {
	out := map[string]string{}
	if m.stor == nil {
		return out
	}
	drives, err := m.stor.List(ctx)
	if err != nil {
		return out
	}
	for _, d := range drives {
		if d.GetAdopted() {
			out[d.GetDriveId()] = d.GetDisk()
		}
	}
	return out
}

// Branch is one place a Space's files live on this Node: a Drive's Space
// folder, or a peer's Pool.
type Branch struct {
	Path string
	// Disk tells branches on the same physical disk apart; a peer's Pool
	// uses its Node id.
	Disk   string
	Remote bool
}

// plan is a Space's copy settings and branches, worked out once per pass.
type plan struct {
	space    *maitrev1.Space
	want     int
	branches []Branch
	// maxCopies is the branches on separate disks here (decision 259).
	maxCopies int
}

// plans lists the Spaces that keep copies and where, on this Node.
func (m *Manager) plans(ctx context.Context) ([]plan, error) {
	spaces, err := storage.Spaces(ctx, m.st)
	if err != nil {
		return nil, err
	}
	records, err := store.ListLive(ctx, m.st, storage.KindDrive, func() *maitrev1.DriveRecord { return &maitrev1.DriveRecord{} })
	if err != nil {
		return nil, err
	}
	disks := m.DiskOf(ctx)
	var out []plan
	for _, b := range spaces {
		want := storage.Copies(b)
		p := plan{space: b, want: want}
		seen := map[string]bool{}
		for _, r := range records {
			if r.GetNodeId() != m.self || storage.RoleOf(r) == storage.RoleParity || r.GetDraining() || r.GetReplacedBy() != "" {
				continue
			}
			mine := false
			for _, id := range storage.SpacesOf(r, spaces) {
				mine = mine || id == b.GetSpaceId()
			}
			dir := filepath.Join(m.Mounts, r.GetDriveId())
			if !mine || !m.Mounted(dir) {
				continue
			}
			disk := disks[r.GetDriveId()]
			if disk == "" {
				disk = r.GetDriveId()
			}
			p.branches = append(p.branches, Branch{Path: filepath.Join(dir, b.GetSpaceId()), Disk: disk})
			seen[disk] = true
		}
		p.maxCopies = len(seen)
		if b.GetSpanNodes() && b.GetCopiesOnDifferentNodes() {
			entries, _ := os.ReadDir(m.Remote)
			for _, e := range entries {
				rp := filepath.Join(m.Remote, e.Name(), b.GetSpaceId())
				if e.IsDir() && m.Mounted(rp) {
					p.branches = append(p.branches, Branch{Path: rp, Disk: "node:" + e.Name(), Remote: true})
				}
			}
		}
		if want > 1 && len(p.branches) > 0 {
			out = append(out, p)
		}
		m.mu.Lock()
		st := m.status[b.GetSpaceId()]
		if st == nil {
			st = &maitrev1.CopiesStatus{SpaceId: b.GetSpaceId()}
			m.status[b.GetSpaceId()] = st
		}
		st.MaxCopies = uint32(p.maxCopies)
		m.mu.Unlock()
	}
	return out, nil
}

// Pass walks every Space that keeps copies and makes each file's count.
func (m *Manager) Pass(ctx context.Context) error {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return errors.New("a copy pass is running")
	}
	m.running = true
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.running = false
		m.mu.Unlock()
	}()
	plans, err := m.plans(ctx)
	if err != nil {
		return err
	}
	for _, p := range plans {
		m.mu.Lock()
		m.status[p.space.GetSpaceId()].Running = true
		m.mu.Unlock()
		short := m.passSpace(ctx, p)
		m.mu.Lock()
		st := m.status[p.space.GetSpaceId()]
		st.Running, st.ShortFiles, st.LastPassUnixMs = false, uint32(short), m.Now().UnixMilli()
		m.mu.Unlock()
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return nil
}

// passSpace brings one Space's files to their count and returns how many
// stayed short.
func (m *Manager) passSpace(ctx context.Context, p plan) int {
	// Every relative path across the local branches (a peer's Pool is a
	// destination, not a source to walk: its own Node keeps its copies).
	rels := map[string]bool{}
	for _, b := range p.branches {
		if b.Remote {
			continue
		}
		_ = filepath.WalkDir(b.Path, func(path string, d fs.DirEntry, err error) error {
			if err != nil || ctx.Err() != nil {
				return nil
			}
			if d.Type().IsRegular() {
				rel, _ := filepath.Rel(b.Path, path)
				if !strings.HasPrefix(rel, ".maitre") {
					rels[rel] = true
				}
			}
			return nil
		})
	}
	short := 0
	for rel := range rels {
		if ctx.Err() != nil {
			return short
		}
		if got, err := m.ensure(ctx, p, rel); err != nil {
			m.log.Warn("copies", "space", p.space.GetSpaceId(), "file", rel, "err", err)
			short++
		} else if got < p.want {
			short++
		}
	}
	return short
}

type copyInfo struct {
	branch Branch
	info   os.FileInfo
}

// ensure makes one file's copies: finds them, picks the newest as the
// source, refreshes stale ones, and adds copies on other disks until the
// count is met or no disk is left. Returns how many good copies it ends
// with.
func (m *Manager) ensure(ctx context.Context, p plan, rel string) (int, error) {
	var have []copyInfo
	for _, b := range p.branches {
		if st, err := os.Lstat(filepath.Join(b.Path, rel)); err == nil && st.Mode().IsRegular() {
			have = append(have, copyInfo{b, st})
		}
	}
	if len(have) == 0 {
		return 0, nil // gone between the walk and now
	}
	sort.SliceStable(have, func(i, j int) bool { return have[i].info.ModTime().After(have[j].info.ModTime()) })
	src := have[0]
	srcPath := filepath.Join(src.branch.Path, rel)
	good := 1
	disks := map[string]bool{src.branch.Disk: true}
	// Stale copies: same path, older or another size. Two copies on one
	// disk count once: a failed disk takes both (decision 250).
	for _, c := range have[1:] {
		if !c.info.ModTime().Equal(src.info.ModTime()) || c.info.Size() != src.info.Size() {
			if err := m.copy(ctx, srcPath, filepath.Join(c.branch.Path, rel), src.info); err != nil {
				return good, err
			}
		}
		if !disks[c.branch.Disk] {
			good++
			disks[c.branch.Disk] = true
		}
	}
	if good >= p.want {
		return good, nil
	}
	// New copies: a branch on a disk without one, a peer first when the
	// Space wants copies on different Nodes, then the roomiest.
	var cands []Branch
	for _, b := range p.branches {
		if disks[b.Disk] {
			continue
		}
		if _, err := os.Lstat(filepath.Join(b.Path, rel)); err == nil {
			continue // something else is there already (a folder, a link)
		}
		cands = append(cands, b)
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].Remote != cands[j].Remote {
			return cands[i].Remote
		}
		return m.Free(cands[i].Path) > m.Free(cands[j].Path)
	})
	for _, b := range cands {
		if good >= p.want {
			break
		}
		if m.Free(b.Path) < uint64(src.info.Size())+1<<30 {
			continue
		}
		if err := m.copy(ctx, srcPath, filepath.Join(b.Path, rel), src.info); err != nil {
			return good, err
		}
		good++
		disks[b.Disk] = true
	}
	return good, nil
}

// copy writes src to dst (through a temporary name), keeping mode, owner
// and times, and reads dst back against src's hash.
func (m *Manager) copy(ctx context.Context, src, dst string, info os.FileInfo) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	m.mirrorDirs(src, dst)
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := filepath.Join(filepath.Dir(dst), ".maitre-copy-"+filepath.Base(dst))
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return err
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(out, h), in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if ctx.Err() != nil {
		os.Remove(tmp)
		return ctx.Err()
	}
	// Read it back: the copy is only a copy once it matches.
	back, err := os.Open(tmp)
	if err != nil {
		os.Remove(tmp)
		return err
	}
	h2 := sha256.New()
	_, err = io.Copy(h2, back)
	back.Close()
	if err != nil || string(h.Sum(nil)) != string(h2.Sum(nil)) {
		os.Remove(tmp)
		if err == nil {
			err = fmt.Errorf("the copy of %s read back differently", filepath.Base(src))
		}
		return err
	}
	chownLike(tmp, info)
	_ = os.Chmod(tmp, info.Mode().Perm())
	_ = os.Chtimes(tmp, info.ModTime(), info.ModTime())
	return os.Rename(tmp, dst)
}

// mirrorDirs gives dst's parent folders src's parents' mode and owner,
// as far as they share a relative path.
func (m *Manager) mirrorDirs(src, dst string) {
	s, d := filepath.Dir(src), filepath.Dir(dst)
	for i := 0; i < 64 && filepath.Base(s) == filepath.Base(d); i++ {
		if info, err := os.Stat(s); err == nil {
			_ = os.Chmod(d, info.Mode().Perm())
			chownLike(d, info)
		}
		s, d = filepath.Dir(s), filepath.Dir(d)
	}
}

// Changed notes a file written or moved into a Space folder (the watcher
// calls it); it is copied once Quiet has passed without another change.
func (m *Manager) Changed(path string) {
	m.mu.Lock()
	m.pending[path] = m.Now().Add(Quiet)
	m.mu.Unlock()
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

// Run copies changed files as they go quiet and runs a full pass every
// hour (and once at start) until ctx ends.
func (m *Manager) Run(ctx context.Context) {
	hourly := time.NewTicker(time.Hour)
	defer hourly.Stop()
	timer := time.NewTimer(time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-hourly.C:
			if err := m.Pass(ctx); err != nil && ctx.Err() == nil {
				m.log.Warn("copy pass", "err", err)
			}
		case <-m.wake:
		case <-timer.C:
		}
		next := m.flush(ctx)
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(next)
	}
}

// flush copies the pending files whose quiet time has passed and says how
// long until the next one is due.
func (m *Manager) flush(ctx context.Context) time.Duration {
	now := m.Now()
	var due []string
	next := time.Minute
	m.mu.Lock()
	for p, at := range m.pending {
		if !at.After(now) {
			due = append(due, p)
			delete(m.pending, p)
		} else if d := at.Sub(now); d < next {
			next = d
		}
	}
	m.mu.Unlock()
	if len(due) == 0 {
		return next
	}
	plans, err := m.plans(ctx)
	if err != nil {
		return next
	}
	for _, path := range due {
		for _, p := range plans {
			for _, b := range p.branches {
				if b.Remote || !strings.HasPrefix(path, b.Path+string(filepath.Separator)) {
					continue
				}
				rel, _ := filepath.Rel(b.Path, path)
				if _, err := m.ensure(ctx, p, rel); err != nil {
					m.log.Warn("copy after a change", "file", rel, "err", err)
				}
			}
		}
	}
	return next
}

// Statuses is each Space's copies on this Node, for the gossip.
func (m *Manager) Statuses() []*maitrev1.CopiesStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*maitrev1.CopiesStatus
	for _, st := range m.status {
		out = append(out, proto.Clone(st).(*maitrev1.CopiesStatus))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SpaceId < out[j].SpaceId })
	return out
}

// Roots are the local Space folders the watcher should follow: those of
// Spaces that keep copies.
func (m *Manager) Roots(ctx context.Context) []string {
	plans, err := m.plans(ctx)
	if err != nil {
		return nil
	}
	var out []string
	for _, p := range plans {
		for _, b := range p.branches {
			if !b.Remote {
				out = append(out, b.Path)
			}
		}
	}
	return out
}
