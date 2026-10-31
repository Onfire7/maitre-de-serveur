// Package drain empties a Drive without breaking paths (ADR 0009, decision
// 247). Each Space's files on the Drive move to that Space's other Drives
// on this Node, chosen by the Space's placement; a Spanning Space without
// room here moves to its Pool on another Node, with a warning; a
// non-Spanning Space with nowhere to go blocks the Drain (decision 246).
// Unsorted files and Orphan Folders move into a Space the user picks,
// under "From <Drive name>/". The Drive stays in its Pools as a no-create
// branch while it drains (storage.LocalBranches), so Services keep seeing
// their files, and leaves each Pool once none of that Space's files remain.
// Files are copied with owners, ACLs, xattrs and hard links, checked by
// checksum, then removed from the Drive. A Service with files on the Drive
// is stopped, its files moved and started again (decision 242). Jobs
// survive a restart of the agent and can be cancelled.
//
// Drain runs for a Drive becoming a Parity Drive, for a rule or tag change
// that takes a Drive out of a Space (Sweep), and for removing a Drive.
package drain

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/layout"
	"github.com/Onfire7/maitre-de-serveur/server/internal/pool"
	"github.com/Onfire7/maitre-de-serveur/server/internal/storage"
	"github.com/Onfire7/maitre-de-serveur/server/internal/store"
)

// Purposes a Drain is started for (DrainDriveRequest.purpose).
const (
	PurposeParity = "parity"
	PurposeRemove = "remove"
	PurposeRule   = "rule"
)

// Services is what Drain needs of the Service manager: the Services on
// this Node with volumes in a Space (names), and stopping and starting
// one. Nil means no Services run here.
type Services interface {
	InSpace(ctx context.Context, spaceID string) []string
	Stop(ctx context.Context, name string) error
	Start(ctx context.Context, name string) error
}

// Copier copies src to dst (both files, or both directories when dir) and
// reports bytes copied so far. The default runs rsync.
type Copier func(ctx context.Context, src, dst string, dir bool, progress func(bytes uint64, current string)) error

// Manager runs Drains on one Node, one at a time.
type Manager struct {
	st   *store.Store
	log  *slog.Logger
	self string
	// Mounts is where Drives are mounted (Mounts/<drive_id>); Remote where
	// peers' Pools are (Remote/<node_id>/<space_id>).
	Mounts, Remote string
	// StateFile keeps the jobs across restarts; "" keeps them in memory.
	StateFile string
	Copy      Copier
	Services  Services
	// OnChange runs after a Drive record changed (pool reassembly, sync).
	OnChange func()
	// Then runs once a Drain finished well, by purpose: making the Drive a
	// Parity Drive, for one. Optional.
	Then func(ctx context.Context, job *maitrev1.DrainJob) error
	// Free reports a directory's free bytes; storage.FreeBytes by default.
	Free func(dir string) uint64
	// Mounted reports whether a directory is a mount point.
	Mounted func(dir string) bool

	mu     sync.Mutex
	jobs   map[string]*job
	order  []string
	queue  chan *job
	seq    int
	cancel map[string]context.CancelFunc
}

type job struct {
	mu sync.Mutex
	pb *maitrev1.DrainJob
	// looseTo is the Space loose files go to.
	looseTo string
}

// New builds a Manager; self is this Node's id.
func New(st *store.Store, log *slog.Logger, self string) *Manager {
	m := &Manager{
		st: st, log: log, self: self, Mounts: pool.DrivesDir, Remote: storage.RemoteDir,
		Free: storage.FreeBytes, Mounted: pool.Mounted,
		jobs: map[string]*job{}, queue: make(chan *job, 64), cancel: map[string]context.CancelFunc{},
	}
	m.Copy = m.rsync
	return m
}

// Run resumes the jobs left from before and executes queued ones until
// ctx ends.
func (m *Manager) Run(ctx context.Context) {
	m.load()
	for {
		select {
		case <-ctx.Done():
			return
		case j := <-m.queue:
			m.execute(ctx, j)
		}
	}
}

// Plan says what draining a Drive would do. spaceIDs limits it to those
// Spaces (and skips loose files); empty means the whole Drive.
func (m *Manager) Plan(ctx context.Context, driveID, looseTo string, spaceIDs []string) (*maitrev1.DrainPlan, error) {
	rec, spaces, drives, err := m.world(ctx, driveID)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(m.Mounts, driveID)
	if !m.Mounted(dir) {
		return nil, fmt.Errorf("%s is not mounted", rec.GetName())
	}
	only := map[string]bool{}
	for _, id := range spaceIDs {
		only[id] = true
	}
	plan := &maitrev1.DrainPlan{}
	byID := map[string]*maitrev1.Space{}
	for _, b := range spaces {
		byID[b.GetSpaceId()] = b
	}
	// Other Drives of each Space on this Node, with their free space. A
	// folder of a Space that exists is that Space's, whether or not the
	// Drive's rules still select it (that is what Sweep drains); only
	// folders of removed Spaces and pre-adoption files are loose.
	var loose []*maitrev1.DriveFolder
	for _, f := range storage.Folders(dir, spaces, allIDs(spaces)) {
		switch f.GetKind() {
		case "space":
			if len(only) > 0 && !only[f.GetSpaceId()] {
				continue
			}
			b := byID[f.GetSpaceId()]
			bytes := treeBytes(filepath.Join(dir, f.GetName()))
			if bytes == 0 {
				continue
			}
			mv := &maitrev1.DrainMove{SpaceId: b.GetSpaceId(), ToSpace: b.GetSpaceId(), Bytes: bytes}
			var free uint64
			for _, d := range drives {
				if d.GetDriveId() == driveID || d.GetDraining() {
					continue
				}
				if !member(d, spaces, b.GetSpaceId()) {
					continue
				}
				odir := filepath.Join(m.Mounts, d.GetDriveId())
				if !m.Mounted(odir) {
					continue
				}
				mv.ToDriveIds = append(mv.ToDriveIds, d.GetDriveId())
				free += m.Free(odir)
			}
			switch {
			case free >= bytes+margin:
			case b.GetSpanNodes():
				node := m.remoteNode(b.GetSpaceId())
				if node == "" {
					plan.Blockers = append(plan.Blockers, fmt.Sprintf("%s's %s of %s has no room on this Node's other Drives, and no other Node's Pool of %s is reachable now.", rec.GetName(), human(bytes), b.GetName(), b.GetName()))
				} else {
					mv.ToNodeId = node
					plan.Warnings = append(plan.Warnings, fmt.Sprintf("%s has no room on this Node's other Drives, so %s moves over the network to another Node's Pool. That takes a while, and Services here then read those files over the network.", b.GetName(), human(bytes-free)))
				}
			case len(mv.ToDriveIds) == 0:
				plan.Blockers = append(plan.Blockers, fmt.Sprintf("%s is the only Drive of %s on this Node, and %s does not span Nodes. Add a Drive to %s, or move its files elsewhere first.", rec.GetName(), b.GetName(), b.GetName(), b.GetName()))
			default:
				plan.Blockers = append(plan.Blockers, fmt.Sprintf("%s's other Drives have %s free, but %s holds %s of it. Add a Drive to %s or free some space first.", b.GetName(), human(free), rec.GetName(), human(bytes), b.GetName()))
			}
			plan.Moves = append(plan.Moves, mv)
			plan.Bytes += bytes
		default:
			if len(only) == 0 {
				loose = append(loose, f)
			}
		}
	}
	if len(loose) > 0 {
		var bytes uint64
		for _, f := range loose {
			bytes += treeBytes(filepath.Join(dir, f.GetName()))
		}
		mv := &maitrev1.DrainMove{ToSpace: looseTo, Bytes: bytes}
		switch {
		case looseTo == "":
			plan.Blockers = append(plan.Blockers, fmt.Sprintf("%s holds %s of files that belong to no Space (%s). Pick a Space to move them into.", rec.GetName(), human(bytes), names(loose)))
		case byID[looseTo] == nil:
			plan.Blockers = append(plan.Blockers, fmt.Sprintf("There is no Space %q for the loose files.", looseTo))
		default:
			var free uint64
			for _, d := range drives {
				if d.GetDriveId() == driveID || d.GetDraining() || !member(d, spaces, looseTo) {
					continue
				}
				odir := filepath.Join(m.Mounts, d.GetDriveId())
				if m.Mounted(odir) {
					mv.ToDriveIds = append(mv.ToDriveIds, d.GetDriveId())
					free += m.Free(odir)
				}
			}
			if len(mv.ToDriveIds) == 0 {
				plan.Blockers = append(plan.Blockers, fmt.Sprintf("%s has no other Drive on this Node to take the loose files.", byID[looseTo].GetName()))
			} else if free < bytes+margin {
				plan.Blockers = append(plan.Blockers, fmt.Sprintf("%s's Drives have %s free; the loose files need %s.", byID[looseTo].GetName(), human(free), human(bytes)))
			}
		}
		plan.Moves = append(plan.Moves, mv)
		plan.Bytes += bytes
	}
	return plan, nil
}

// margin is what a destination keeps free beyond the files moved.
const margin = 1 << 30

// Start plans and queues a Drain of a whole Drive. confirm must be the
// Drive's name. Refused while the plan has blockers or the Drive is
// already draining.
func (m *Manager) Start(ctx context.Context, driveID, looseTo, confirm, purpose string) (*maitrev1.DrainJob, error) {
	rec, _, _, err := m.world(ctx, driveID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(confirm) != rec.GetName() {
		return nil, fmt.Errorf("to empty it, repeat exactly: %s", rec.GetName())
	}
	return m.start(ctx, rec, looseTo, purpose, nil)
}

func (m *Manager) start(ctx context.Context, rec *maitrev1.DriveRecord, looseTo, purpose string, spaceIDs []string) (*maitrev1.DrainJob, error) {
	if j := m.active(rec.GetDriveId()); j != nil {
		return nil, fmt.Errorf("%s is already being emptied", rec.GetName())
	}
	plan, err := m.Plan(ctx, rec.GetDriveId(), looseTo, spaceIDs)
	if err != nil {
		return nil, err
	}
	if len(plan.Blockers) > 0 {
		return nil, errors.New(strings.Join(plan.Blockers, " "))
	}
	j := &job{pb: &maitrev1.DrainJob{DriveId: rec.GetDriveId(), State: "queued", Purpose: purpose, SpaceIds: spaceIDs, Plan: plan, BytesTotal: plan.GetBytes()}, looseTo: looseTo}
	for _, w := range plan.Warnings {
		j.pb.Notes = append(j.pb.Notes, w)
	}
	m.mu.Lock()
	m.seq++
	j.pb.JobId = fmt.Sprintf("drain-%d-%d", time.Now().Unix(), m.seq)
	m.jobs[j.pb.JobId] = j
	m.order = append(m.order, j.pb.JobId)
	m.mu.Unlock()
	m.save()
	select {
	case m.queue <- j:
	default:
		m.setState(j, "failed", "too many queued jobs")
	}
	return m.snapshot(j), nil
}

// Sweep starts a Drain for each of this Node's Drives whose folder of a
// Space its rules no longer select still holds files (a tag or rule
// change, decision 247), scoped to those Spaces. Drives that are already
// draining, Parity Drives and blocked cases are left alone; a blocked
// case is logged once per change.
func (m *Manager) Sweep(ctx context.Context) {
	spaces, err := storage.Spaces(ctx, m.st)
	if err != nil {
		return
	}
	records, err := store.ListLive(ctx, m.st, storage.KindDrive, func() *maitrev1.DriveRecord { return &maitrev1.DriveRecord{} })
	if err != nil {
		return
	}
	for _, r := range records {
		if r.GetNodeId() != m.self || storage.RoleOf(r) == storage.RoleParity || m.active(r.GetDriveId()) != nil {
			continue
		}
		dir := filepath.Join(m.Mounts, r.GetDriveId())
		if !m.Mounted(dir) {
			continue
		}
		mine := map[string]bool{}
		for _, id := range storage.SpacesOf(r, spaces) {
			mine[id] = true
		}
		var left []string
		for _, b := range spaces {
			if !mine[b.GetSpaceId()] && storage.HasFiles(filepath.Join(dir, b.GetSpaceId())) {
				left = append(left, b.GetSpaceId())
			}
		}
		if len(left) == 0 {
			continue
		}
		if _, err := m.start(ctx, r, "", PurposeRule, left); err != nil {
			m.log.Warn("drain after a rule change cannot start", "drive", r.GetName(), "spaces", strings.Join(left, ","), "why", err)
		} else {
			m.log.Info("draining after a rule change", "drive", r.GetName(), "spaces", strings.Join(left, ","))
		}
	}
}

// Jobs lists every Drain, newest last.
func (m *Manager) Jobs() []*maitrev1.DrainJob {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*maitrev1.DrainJob
	for _, id := range m.order {
		out = append(out, m.snapshot(m.jobs[id]))
	}
	return out
}

// Cancel stops a queued or running Drain.
func (m *Manager) Cancel(id string) error {
	m.mu.Lock()
	j := m.jobs[id]
	c := m.cancel[id]
	m.mu.Unlock()
	if j == nil {
		return errors.New("no such job")
	}
	j.mu.Lock()
	state := j.pb.State
	j.mu.Unlock()
	if state == "queued" {
		m.setState(j, "cancelled", "")
		m.save()
		return nil
	}
	if c != nil {
		c()
	}
	return nil
}

// active is the queued or running Drain of a Drive, if any.
func (m *Manager) active(driveID string) *job {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, j := range m.jobs {
		j.mu.Lock()
		hit := j.pb.DriveId == driveID && (j.pb.State == "queued" || j.pb.State == "running")
		j.mu.Unlock()
		if hit {
			return j
		}
	}
	return nil
}

func (m *Manager) world(ctx context.Context, driveID string) (*maitrev1.DriveRecord, []*maitrev1.Space, []*maitrev1.DriveRecord, error) {
	var rec maitrev1.DriveRecord
	if _, err := m.st.Get(ctx, storage.KindDrive, driveID, &rec); err != nil {
		return nil, nil, nil, fmt.Errorf("no Drive %s", driveID)
	}
	if rec.GetNodeId() != m.self {
		return nil, nil, nil, fmt.Errorf("%s is a Drive of another Node", rec.GetName())
	}
	spaces, err := storage.Spaces(ctx, m.st)
	if err != nil {
		return nil, nil, nil, err
	}
	records, err := store.ListLive(ctx, m.st, storage.KindDrive, func() *maitrev1.DriveRecord { return &maitrev1.DriveRecord{} })
	if err != nil {
		return nil, nil, nil, err
	}
	var mine []*maitrev1.DriveRecord
	for _, r := range records {
		if r.GetNodeId() == m.self {
			mine = append(mine, r)
		}
	}
	return &rec, spaces, mine, nil
}

func allIDs(spaces []*maitrev1.Space) []string {
	out := make([]string, len(spaces))
	for i, b := range spaces {
		out[i] = b.GetSpaceId()
	}
	return out
}

func member(d *maitrev1.DriveRecord, spaces []*maitrev1.Space, spaceID string) bool {
	for _, id := range storage.SpacesOf(d, spaces) {
		if id == spaceID {
			return true
		}
	}
	return false
}

// remoteNode is a Node whose Pool of a Space is mounted here, "" if none.
func (m *Manager) remoteNode(spaceID string) string {
	entries, _ := os.ReadDir(m.Remote)
	for _, e := range entries {
		if e.IsDir() && m.Mounted(filepath.Join(m.Remote, e.Name(), spaceID)) {
			return e.Name()
		}
	}
	return ""
}

func (m *Manager) snapshot(j *job) *maitrev1.DrainJob {
	j.mu.Lock()
	defer j.mu.Unlock()
	return proto.Clone(j.pb).(*maitrev1.DrainJob)
}

func (m *Manager) setState(j *job, state, msg string) {
	j.mu.Lock()
	j.pb.State, j.pb.Message = state, msg
	if state == "running" {
		j.pb.StartedUnixMs = time.Now().UnixMilli()
	} else if state != "queued" {
		j.pb.FinishedUnixMs = time.Now().UnixMilli()
	}
	j.mu.Unlock()
}

func (m *Manager) note(j *job, format string, args ...any) {
	j.mu.Lock()
	j.pb.Notes = append(j.pb.Notes, fmt.Sprintf(format, args...))
	j.mu.Unlock()
	m.save()
}

// execute runs one Drain.
func (m *Manager) execute(parent context.Context, j *job) {
	j.mu.Lock()
	if j.pb.State != "queued" {
		j.mu.Unlock()
		return
	}
	driveID := j.pb.DriveId
	j.mu.Unlock()
	ctx, cancel := context.WithCancel(parent)
	m.mu.Lock()
	m.cancel[j.pb.JobId] = cancel
	m.mu.Unlock()
	defer func() {
		cancel()
		m.mu.Lock()
		delete(m.cancel, j.pb.JobId)
		m.mu.Unlock()
	}()
	m.setState(j, "running", "")
	m.save()
	bg := context.WithoutCancel(parent)
	if err := m.setDraining(bg, driveID, true); err != nil {
		m.setState(j, "failed", err.Error())
		m.save()
		return
	}
	err := m.drain(ctx, j)
	// The Drive takes files again unless it is now empty for a purpose
	// that takes it out of its Spaces (its Then does that).
	if clearErr := m.setDraining(bg, driveID, false); clearErr != nil && err == nil {
		err = clearErr
	}
	switch {
	case err == nil:
		m.setState(j, "done", "")
		if m.Then != nil {
			if err := m.Then(bg, m.snapshot(j)); err != nil {
				m.setState(j, "failed", "emptied, but then: "+err.Error())
			}
		}
	case ctx.Err() != nil:
		m.setState(j, "cancelled", "stopped; files already moved stay where they went, the rest are still on the Drive")
	default:
		m.setState(j, "failed", err.Error()+"; files already moved stay where they went, the rest are still on the Drive")
		m.log.Warn("drain failed", "job", j.pb.JobId, "err", err)
	}
	m.save()
}

// setDraining marks the Drive's record, so Pools make it no-create.
func (m *Manager) setDraining(ctx context.Context, driveID string, on bool) error {
	var rec maitrev1.DriveRecord
	if _, err := m.st.Get(ctx, storage.KindDrive, driveID, &rec); err != nil {
		return fmt.Errorf("no Drive %s", driveID)
	}
	if rec.GetDraining() == on {
		return nil
	}
	rec.Draining = on
	if _, err := m.st.Put(ctx, storage.KindDrive, driveID, &rec, false); err != nil {
		return err
	}
	if m.OnChange != nil {
		m.OnChange()
	}
	return nil
}

// drain moves everything the plan names off the Drive.
func (m *Manager) drain(ctx context.Context, j *job) error {
	j.mu.Lock()
	driveID, plan := j.pb.DriveId, proto.Clone(j.pb.Plan).(*maitrev1.DrainPlan)
	j.mu.Unlock()
	rec, spaces, _, err := m.world(ctx, driveID)
	if err != nil {
		return err
	}
	dir := filepath.Join(m.Mounts, driveID)
	byID := map[string]*maitrev1.Space{}
	for _, b := range spaces {
		byID[b.GetSpaceId()] = b
	}
	var done uint64
	for _, mv := range plan.GetMoves() {
		if mv.GetSpaceId() != "" {
			b := byID[mv.GetSpaceId()]
			if b == nil {
				continue
			}
			src := filepath.Join(dir, b.GetSpaceId())
			dests := m.dests(mv, b)
			if err := m.moveSpace(ctx, j, b, src, dests, &done); err != nil {
				return fmt.Errorf("%s: %w", b.GetName(), err)
			}
			// Only the marker left: the folder goes, and the Drive leaves
			// the Pool at the next assembly.
			if !storage.HasFiles(src) {
				_ = os.RemoveAll(src)
			}
			continue
		}
		// Loose files: into <Space>/From <Drive>/ on one of the Space's Drives.
		dests := m.dests(mv, byID[mv.GetToSpace()])
		if len(dests) == 0 {
			return fmt.Errorf("no Drive of %s to take the loose files", mv.GetToSpace())
		}
		for _, f := range storage.Folders(dir, spaces, allIDs(spaces)) {
			if f.GetKind() == "space" {
				continue
			}
			src := filepath.Join(dir, f.GetName())
			dst := filepath.Join(m.pick(dests, treeBytes(src)), mv.GetToSpace(), "From "+rec.GetName(), f.GetName())
			if err := m.moveEntry(ctx, j, src, dst, &done); err != nil {
				return fmt.Errorf("%s: %w", f.GetName(), err)
			}
		}
	}
	return nil
}

// dests are the branch roots a move may fill: this Node's Drives named by
// the plan, or the remote Pool when the plan sends the Space there.
func (m *Manager) dests(mv *maitrev1.DrainMove, b *maitrev1.Space) []string {
	if mv.GetToNodeId() != "" {
		return []string{filepath.Join(m.Remote, mv.GetToNodeId())}
	}
	var out []string
	for _, id := range mv.GetToDriveIds() {
		out = append(out, filepath.Join(m.Mounts, id))
	}
	return out
}

// pick is the destination root with the most free space for a tree of
// bytes; with one candidate, that one.
func (m *Manager) pick(dests []string, bytes uint64) string {
	best, bestFree := dests[0], uint64(0)
	for _, d := range dests {
		if f := m.Free(d); f > bestFree {
			best, bestFree = d, f
		}
	}
	return best
}

// moveSpace moves a Space folder's contents to the Space's folders on
// dests. Each top-level entry goes whole to the Drive with the most room
// when it fits; a larger one goes file by file. Services with volumes in
// the Space are stopped while their folders move (decision 242).
func (m *Manager) moveSpace(ctx context.Context, j *job, b *maitrev1.Space, src string, dests []string, done *uint64) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	var stopped []string
	defer func() {
		for _, name := range stopped {
			if err := m.Services.Start(context.WithoutCancel(ctx), name); err != nil {
				m.log.Warn("drain: start service", "service", name, "err", err)
			} else {
				m.note(j, "%s started again.", name)
			}
		}
	}()
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".maitre") {
			continue
		}
		if name == layout.ServicesDir && m.Services != nil && len(stopped) == 0 {
			for _, svc := range m.Services.InSpace(ctx, b.GetSpaceId()) {
				if _, err := os.Stat(filepath.Join(src, layout.ServiceDir(svc))); err != nil {
					continue
				}
				if err := m.Services.Stop(ctx, svc); err != nil {
					return fmt.Errorf("stop %s: %w", svc, err)
				}
				stopped = append(stopped, svc)
				m.note(j, "%s stopped while its files move.", svc)
			}
		}
		if err := m.moveTree(ctx, j, filepath.Join(src, name), dests, b.GetSpaceId(), name, done); err != nil {
			return err
		}
	}
	return nil
}

// moveTree moves one entry of a Space folder: whole to the roomiest
// destination when it fits, else each child in turn (a file that fits
// nowhere fails the Drain).
func (m *Manager) moveTree(ctx context.Context, j *job, src string, dests []string, spaceID, rel string, done *uint64) error {
	bytes := treeBytes(src)
	dest := m.pick(dests, bytes)
	if m.Free(dest) >= bytes+margin {
		return m.moveEntry(ctx, j, src, filepath.Join(dest, spaceID, rel), done)
	}
	st, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return fmt.Errorf("no Drive has room for %s (%s)", rel, human(bytes))
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := m.moveTree(ctx, j, filepath.Join(src, e.Name()), dests, spaceID, filepath.Join(rel, e.Name()), done); err != nil {
			return err
		}
	}
	// Empty now: the folder itself, with its attributes, lands too.
	for _, d := range dests {
		if _, err := os.Stat(filepath.Join(d, spaceID, rel)); err == nil {
			return os.Remove(src)
		}
	}
	return m.moveEntry(ctx, j, src, filepath.Join(dest, spaceID, rel), done)
}

// moveEntry copies one file or folder, checks it, and removes the source.
// A destination that exists already is merged into (folders) or kept as
// the newer of the two (files).
func (m *Manager) moveEntry(ctx context.Context, j *job, src, dst string, done *uint64) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	st, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if !st.IsDir() {
		if other, err := os.Lstat(dst); err == nil && other.ModTime().After(st.ModTime()) {
			// A newer copy is already there (Copies, or a move before):
			// the Drive's is the stale one.
			*done += uint64(st.Size())
			return os.Remove(src)
		}
	}
	base := *done
	err = m.Copy(ctx, src, dst, st.IsDir(), func(bytes uint64, current string) {
		j.mu.Lock()
		j.pb.BytesDone, j.pb.Current = base+bytes, current
		j.mu.Unlock()
	})
	if err != nil {
		return err
	}
	*done = base + treeBytes(src)
	j.mu.Lock()
	j.pb.BytesDone, j.pb.Current = *done, ""
	j.mu.Unlock()
	m.save()
	return os.RemoveAll(src)
}

var progressRe = regexp.MustCompile(`^\s*([\d,]+)\s+\d+%`)

// rsync is the default Copier: owners, ACLs, xattrs and hard links kept
// (-aHAX), then a checksum pass that confirms every byte arrived.
func (m *Manager) rsync(ctx context.Context, src, dst string, dir bool, progress func(uint64, string)) error {
	s, d := src, dst
	if dir {
		s, d = src+"/", dst+"/"
	}
	run := func(extra ...string) error {
		args := append([]string{"-aHAX", "--info=progress2", "--no-inc-recursive"}, extra...)
		cmd := exec.CommandContext(ctx, "rsync", append(args, s, d)...)
		out, err := cmd.StdoutPipe()
		if err != nil {
			return err
		}
		if err := cmd.Start(); err != nil {
			return err
		}
		sc := bufio.NewScanner(out)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		sc.Split(scanLinesOrCR)
		var n uint64
		for sc.Scan() {
			line := sc.Text()
			if mm := progressRe.FindStringSubmatch(line); mm != nil {
				n, _ = strconv.ParseUint(strings.ReplaceAll(mm[1], ",", ""), 10, 64)
				progress(n, "")
			} else if t := strings.TrimSpace(line); t != "" && !strings.HasPrefix(t, "sending") && !strings.HasPrefix(t, "sent ") && !strings.HasPrefix(t, "total ") {
				progress(n, t)
			}
		}
		if err := cmd.Wait(); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("rsync: %w", err)
		}
		return nil
	}
	if err := run(); err != nil {
		return err
	}
	return run("-c")
}

func scanLinesOrCR(data []byte, atEOF bool) (advance int, token []byte, err error) {
	for i, b := range data {
		if b == '\n' || b == '\r' {
			return i + 1, data[:i], nil
		}
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

// treeBytes is the size of a file or everything under a folder.
func treeBytes(p string) uint64 {
	var n uint64
	_ = filepath.WalkDir(p, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			n += uint64(info.Size())
		}
		return nil
	})
	return n
}

func names(fs []*maitrev1.DriveFolder) string {
	var out []string
	for i, f := range fs {
		if i == 3 {
			out = append(out, fmt.Sprintf("and %d more", len(fs)-3))
			break
		}
		out = append(out, f.GetName())
	}
	return strings.Join(out, ", ")
}

func human(b uint64) string {
	switch {
	case b >= 1<<40:
		return fmt.Sprintf("%.1f TB", float64(b)/float64(1<<40))
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(b)/float64(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(b)/float64(1<<20))
	default:
		return fmt.Sprintf("%d KB", (b+1023)/1024)
	}
}

// The jobs file: every job's public state and where loose files go.
type saved struct {
	Jobs []savedJob `json:"jobs"`
}
type savedJob struct {
	Job     []byte `json:"job"`
	LooseTo string `json:"loose_to"`
}

func (m *Manager) save() {
	if m.StateFile == "" {
		return
	}
	m.mu.Lock()
	var out saved
	for _, id := range m.order {
		j := m.jobs[id]
		pb := m.snapshot(j)
		raw, _ := proto.Marshal(pb)
		out.Jobs = append(out.Jobs, savedJob{Job: raw, LooseTo: j.looseTo})
	}
	m.mu.Unlock()
	data, _ := json.Marshal(out)
	tmp := m.StateFile + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err == nil {
		_ = os.Rename(tmp, m.StateFile)
	}
}

// load reads the jobs from before and queues those that had not finished
// (a running one starts over; what it moved already is gone from the
// Drive, so it has less to do).
func (m *Manager) load() {
	if m.StateFile == "" {
		return
	}
	data, err := os.ReadFile(m.StateFile)
	if err != nil {
		return
	}
	var in saved
	if json.Unmarshal(data, &in) != nil {
		return
	}
	m.mu.Lock()
	var resume []*job
	for _, sj := range in.Jobs {
		pb := &maitrev1.DrainJob{}
		if proto.Unmarshal(sj.Job, pb) != nil || m.jobs[pb.JobId] != nil {
			continue // a job started before Run read the file is already here
		}
		j := &job{pb: pb, looseTo: sj.LooseTo}
		m.jobs[pb.JobId] = j
		m.order = append(m.order, pb.JobId)
		if pb.State == "queued" || pb.State == "running" {
			pb.State = "queued"
			resume = append(resume, j)
		}
		if n, err := strconv.Atoi(pb.JobId[strings.LastIndex(pb.JobId, "-")+1:]); err == nil && n > m.seq {
			m.seq = n
		}
	}
	m.mu.Unlock()
	sort.SliceStable(resume, func(i, k int) bool { return resume[i].pb.StartedUnixMs < resume[k].pb.StartedUnixMs })
	for _, j := range resume {
		select {
		case m.queue <- j:
		default:
		}
	}
}
