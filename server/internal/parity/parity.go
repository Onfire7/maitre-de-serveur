// Package parity runs SnapRAID for one Node (ADR 0009). Parity is chosen
// per Space (Parity Level 0, 1 or 2) and stored on the Node's Parity
// Drives: one array per level, each with one data entry per Drive at the
// Drive's root and include rules for the Spaces at that level, so other
// Spaces, loose files and .maitre/ stay out (decisions 236, 237). The
// arrays share the Parity Drives: Parity Drive 1 holds every array's first
// parity file, Parity Drive 2 the level-2 array's second. Syncs run on a
// schedule and are held when a Drive is missing or empty or too many files
// were deleted (231); scrubs check a share of the data each time (230).
package parity

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/drain"
	"github.com/Onfire7/maitre-de-serveur/server/internal/pool"
	"github.com/Onfire7/maitre-de-serveur/server/internal/storage"
	"github.com/Onfire7/maitre-de-serveur/server/internal/store"
)

// KindSchedule is the Record kind for ProtectionSchedule bodies; the
// Fleet's default has id ScheduleFleet, a Node's override its node_id.
const (
	KindSchedule  = "protection"
	ScheduleFleet = "fleet"
)

// Limits on automatic syncs (decision 231) and Parity Drives (228).
const (
	MaxParityDrives   = 2
	HeldDeletedShare  = 0.05
	SuggestSecondFrom = 5
)

// ValidateSchedule is what a Node accepts as a ProtectionSchedule record.
func ValidateSchedule(s *maitrev1.ProtectionSchedule) error {
	if t := s.GetSyncTime(); t != "" {
		parts := strings.SplitN(t, ":", 2)
		h, err1 := strconv.Atoi(parts[0])
		mi, err2 := 0, error(nil)
		if len(parts) == 2 {
			mi, err2 = strconv.Atoi(parts[1])
		}
		if len(parts) != 2 || err1 != nil || err2 != nil || h < 0 || h > 23 || mi < 0 || mi > 59 {
			return errors.New("the sync time is HH:MM")
		}
	}
	for _, d := range s.GetSyncDays() {
		if d > 6 {
			return errors.New("sync days are 0 (Sunday) to 6")
		}
	}
	if s.GetScrubPercent() > 100 {
		return errors.New("the scrub share is a percentage")
	}
	if s.GetScrubEveryDays() > 365 {
		return errors.New("scrub at least once a year")
	}
	return nil
}

// Manager keeps one Node's parity.
type Manager struct {
	st     *store.Store
	log    *slog.Logger
	self   string
	stor   *storage.Manager
	drains *drain.Manager
	// Mounts is where Drives are mounted; ConfigDir where the SnapRAID
	// configs are written; StateFile keeps the last sync and scrub times.
	Mounts, ConfigDir, StateFile string
	// Run executes commands (snapraid, rm); a fake in tests.
	Run storage.Runner
	// Mounted reports whether a directory is a mount point.
	Mounted func(dir string) bool
	// Used reports a mounted Drive's used bytes (decision 227's measure).
	Used func(dir string) uint64
	// Now is the clock.
	Now func() time.Time
	// Nice runs commands at idle IO priority when true (false in tests).
	Nice bool

	mu       sync.Mutex
	state    state
	job      string
	jobDrive string
	progress float64
	running  bool
	arrays   []*maitrev1.ParityArray
	cancel   context.CancelFunc
}

type state struct {
	LastSync        int64    `json:"last_sync"`
	LastScrub       int64    `json:"last_scrub"`
	Held            string   `json:"held"`
	Unprotected     uint32   `json:"unprotected"`
	Missing         []string `json:"missing"`
	LastError       string   `json:"last_error"`
	LastAttemptSync int64    `json:"last_attempt_sync"`
}

// New builds a Manager over the storage and drain managers.
func New(st *store.Store, log *slog.Logger, self string, stor *storage.Manager, drains *drain.Manager) *Manager {
	m := &Manager{
		st: st, log: log, self: self, stor: stor, drains: drains,
		Mounts: pool.DrivesDir, ConfigDir: "/run/maitre/snapraid", Run: storage.ExecRunner{},
		Mounted: pool.Mounted, Used: storage.UsedBytes, Now: time.Now, Nice: true,
	}
	if drains != nil {
		drains.Then = m.afterDrain
	}
	return m
}

// Array is one SnapRAID array: a level, its Spaces and its data Drives.
type Array struct {
	Level    int
	Spaces   []*maitrev1.Space
	Drives   []*maitrev1.DriveRecord
	Parity   []*maitrev1.DriveRecord
	LevelGot int
}

// ParityDrives are this Node's Parity Drives, the first made first.
func (m *Manager) ParityDrives(ctx context.Context) ([]*maitrev1.DriveRecord, error) {
	records, err := store.ListLive(ctx, m.st, storage.KindDrive, func() *maitrev1.DriveRecord { return &maitrev1.DriveRecord{} })
	if err != nil {
		return nil, err
	}
	var out []*maitrev1.DriveRecord
	for _, r := range records {
		if r.GetNodeId() == m.self && storage.RoleOf(r) == storage.RoleParity && r.GetReplacedBy() == "" {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].GetAddedUnixMs() < out[j].GetAddedUnixMs() })
	return out, nil
}

// Arrays works out the Node's arrays from its Spaces and Drives: for each
// level, the plain Spaces at that level and this Node's data Drives that
// belong to any of them. An array with no Drives or no Parity Drive is
// left out.
func (m *Manager) Arrays(ctx context.Context) ([]Array, error) {
	spaces, err := storage.Spaces(ctx, m.st)
	if err != nil {
		return nil, err
	}
	records, err := store.ListLive(ctx, m.st, storage.KindDrive, func() *maitrev1.DriveRecord { return &maitrev1.DriveRecord{} })
	if err != nil {
		return nil, err
	}
	parity, _ := m.ParityDrives(ctx)
	var out []Array
	for level := 1; level <= 2; level++ {
		a := Array{Level: level, Parity: parity}
		for _, b := range spaces {
			if int(b.GetParityLevel()) == level && !b.GetMirrored() {
				a.Spaces = append(a.Spaces, b)
			}
		}
		if len(a.Spaces) == 0 {
			continue
		}
		for _, r := range records {
			if r.GetNodeId() != m.self || storage.RoleOf(r) != storage.RoleData || r.GetReplacedBy() != "" {
				continue
			}
			mine := storage.SpacesOf(r, spaces)
			for _, b := range a.Spaces {
				if contains(mine, b.GetSpaceId()) {
					a.Drives = append(a.Drives, r)
					break
				}
			}
		}
		a.LevelGot = level
		if len(parity) < level {
			a.LevelGot = len(parity)
		}
		if len(a.Drives) == 0 || a.LevelGot == 0 {
			continue
		}
		out = append(out, a)
	}
	return out, nil
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// Name is the name SnapRAID knows a Drive by.
func Name(r *maitrev1.DriveRecord) string {
	if n := r.GetSnapraidName(); n != "" {
		return n
	}
	return r.GetDriveId()
}

// Config renders one array's SnapRAID configuration.
func (m *Manager) Config(a Array) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Written by Maître for level %d; do not edit.\n", a.Level)
	tag := fmt.Sprintf("level%d", a.Level)
	for i := 0; i < a.LevelGot && i < len(a.Parity); i++ {
		key := "parity"
		if i > 0 {
			key = fmt.Sprintf("%d-parity", i+1)
		}
		fmt.Fprintf(&b, "%s %s\n", key, filepath.Join(m.Mounts, a.Parity[i].GetDriveId(), storage.ParityDir, tag+"."+key))
	}
	for _, p := range a.Parity[:min(a.LevelGot, len(a.Parity))] {
		fmt.Fprintf(&b, "content %s\n", filepath.Join(m.Mounts, p.GetDriveId(), storage.ParityDir, tag+".content"))
	}
	for _, d := range a.Drives {
		fmt.Fprintf(&b, "content %s\n", filepath.Join(m.Mounts, d.GetDriveId(), storage.ParityDir, tag+".content"))
	}
	for _, d := range a.Drives {
		fmt.Fprintf(&b, "data %s %s/\n", Name(d), filepath.Join(m.Mounts, d.GetDriveId()))
	}
	for _, s := range a.Spaces {
		fmt.Fprintf(&b, "include /%s/\n", s.GetSpaceId())
	}
	b.WriteString("exclude /.maitre/\nexclude *.unrecoverable\nexclude /lost+found/\nautosave 500\n")
	return b.String()
}

func (m *Manager) configPath(level int) string {
	return filepath.Join(m.ConfigDir, fmt.Sprintf("level%d.conf", level))
}

// WriteConfigs writes every array's config and makes room for the content
// files; arrays that no longer exist lose their config.
func (m *Manager) WriteConfigs(ctx context.Context) ([]Array, error) {
	arrays, err := m.Arrays(ctx)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(m.ConfigDir, 0o755); err != nil {
		return nil, err
	}
	have := map[int]bool{}
	for _, a := range arrays {
		have[a.Level] = true
		if err := os.WriteFile(m.configPath(a.Level), []byte(m.Config(a)), 0o644); err != nil {
			return nil, err
		}
		for _, d := range append(append([]*maitrev1.DriveRecord(nil), a.Drives...), a.Parity...) {
			dir := filepath.Join(m.Mounts, d.GetDriveId())
			if m.Mounted(dir) {
				_ = os.MkdirAll(filepath.Join(dir, storage.ParityDir), 0o700)
			}
		}
	}
	for level := 1; level <= 2; level++ {
		if !have[level] {
			_ = os.Remove(m.configPath(level))
		}
	}
	m.mu.Lock()
	m.arrays = nil
	for _, a := range arrays {
		pa := &maitrev1.ParityArray{Level: uint32(a.Level), LevelGot: uint32(a.LevelGot)}
		for _, d := range a.Drives {
			pa.DriveIds = append(pa.DriveIds, d.GetDriveId())
		}
		for _, s := range a.Spaces {
			pa.SpaceIds = append(pa.SpaceIds, s.GetSpaceId())
		}
		m.arrays = append(m.arrays, pa)
	}
	m.mu.Unlock()
	return arrays, nil
}

// Check says whether a Drive (by id) or an unadopted disk (by device) can
// become a Parity Drive, and what that takes.
func (m *Manager) Check(ctx context.Context, driveID, device, looseTo string) (*maitrev1.CheckParityDriveResponse, error) {
	res := &maitrev1.CheckParityDriveResponse{}
	drives, err := m.stor.List(ctx)
	if err != nil {
		return nil, err
	}
	var target *maitrev1.Drive
	for _, d := range drives {
		if (driveID != "" && d.GetDriveId() == driveID) || (driveID == "" && d.GetDevice() == device) {
			target = d
		}
	}
	if target == nil {
		return nil, errors.New("no such Drive or disk on this Node")
	}
	if target.GetSystem() {
		res.Blockers = append(res.Blockers, "That disk holds the running system.")
		return res, nil
	}
	rec := target.GetRecord()
	if rec != nil && storage.RoleOf(rec) == storage.RoleParity {
		res.Blockers = append(res.Blockers, fmt.Sprintf("%s is a Parity Drive already.", rec.GetName()))
		return res, nil
	}
	if rec != nil && storage.RoleOf(rec) == storage.RoleMirrored {
		res.Blockers = append(res.Blockers, fmt.Sprintf("%s is a Mirrored Drive; it already survives a failed disk.", rec.GetName()))
		return res, nil
	}
	parity, _ := m.ParityDrives(ctx)
	if len(parity) >= MaxParityDrives {
		res.Blockers = append(res.Blockers, "This Node has two Parity Drives already, the most a Node takes.")
	}
	// Decision 240: not the last data Drive.
	others := 0
	for _, d := range drives {
		r := d.GetRecord()
		if d.GetAdopted() && d.GetDriveId() != target.GetDriveId() && storage.RoleOf(r) == storage.RoleData && r.GetReplacedBy() == "" {
			others++
		}
		// Decision 239: no other Drive on the same disk.
		if d.GetAdopted() && d.GetDriveId() != target.GetDriveId() && d.GetDisk() != "" && d.GetDisk() == target.GetDisk() {
			res.Blockers = append(res.Blockers, fmt.Sprintf("%s shares its disk with %s. A Parity Drive must be a disk of its own; otherwise one failure takes both.", nameOf(target), d.GetRecord().GetName()))
		}
	}
	if others == 0 {
		res.Blockers = append(res.Blockers, "This Node would have no other Drive to protect.")
	}
	// Decision 227: at least as large as the most protected data on one Drive.
	spaces, _ := storage.Spaces(ctx, m.st)
	protects := map[string]bool{}
	for _, d := range drives {
		r := d.GetRecord()
		if !d.GetAdopted() || d.GetDriveId() == target.GetDriveId() || storage.RoleOf(r) != storage.RoleData {
			continue
		}
		var covered bool
		for _, id := range storage.SpacesOf(r, spaces) {
			for _, b := range spaces {
				if b.GetSpaceId() == id && b.GetParityLevel() > 0 && !b.GetMirrored() {
					covered = true
					protects[id] = true
				}
			}
		}
		if covered {
			if u := m.Used(filepath.Join(m.Mounts, d.GetDriveId())); u > res.LargestProtectedBytes {
				res.LargestProtectedBytes = u
			}
		}
	}
	for id := range protects {
		res.Protects = append(res.Protects, id)
	}
	sort.Strings(res.Protects)
	if target.GetSizeBytes() < res.LargestProtectedBytes {
		res.Blockers = append(res.Blockers, fmt.Sprintf("%s holds %s; a Drive it would protect holds %s. A Parity Drive must be at least as large as the most protected data on any one Drive.", nameOf(target), human(target.GetSizeBytes()), human(res.LargestProtectedBytes)))
	} else if res.LargestProtectedBytes > 0 && target.GetSizeBytes() < res.LargestProtectedBytes+res.LargestProtectedBytes/10 {
		res.Warnings = append(res.Warnings, fmt.Sprintf("%s is barely larger than the most protected data on one Drive (%s). As those Drives fill, protection stops at %s each.", nameOf(target), human(res.LargestProtectedBytes), human(target.GetSizeBytes())))
	}
	if len(protects) == 0 {
		res.Warnings = append(res.Warnings, "No Space on this Node has a Parity Level yet; the Parity Drive protects nothing until one does.")
	}
	if rec != nil && m.drains != nil && len(storage.Leftovers(filepath.Join(m.Mounts, rec.GetDriveId()))) > 0 {
		plan, err := m.drains.Plan(ctx, rec.GetDriveId(), looseTo, nil)
		if err != nil {
			return nil, err
		}
		res.Plan = plan
		res.Blockers = append(res.Blockers, plan.GetBlockers()...)
		res.Warnings = append(res.Warnings, plan.GetWarnings()...)
	}
	if rec == nil && strings.HasPrefix(target.GetLabel(), storage.ParityLabelPrefix) {
		res.Warnings = append(res.Warnings, "This disk holds Maître parity from before; it is kept, not formatted (decision 225).")
	}
	return res, nil
}

func nameOf(d *maitrev1.Drive) string {
	if d.GetRecord() != nil {
		return d.GetRecord().GetName()
	}
	if d.GetModel() != "" {
		return d.GetModel()
	}
	return d.GetDevice()
}

// SetParity turns the toggle. On, for an adopted Drive: a Drain when it
// holds anything, then the format (afterDrain); for a disk not yet
// adopted: formatted now, or adopted as is when it carries Maître parity.
// Off: parity files deleted, the Drive a data Drive again. confirm is the
// Drive's name, or the disk's model or path.
func (m *Manager) SetParity(ctx context.Context, req *maitrev1.SetParityDriveRequest) (*maitrev1.SetParityDriveResponse, error) {
	if !req.GetParity() {
		return m.unsetParity(ctx, req.GetDriveId(), req.GetConfirm())
	}
	check, err := m.Check(ctx, req.GetDriveId(), req.GetDevice(), req.GetLooseToSpace())
	if err != nil {
		return nil, err
	}
	if len(check.Blockers) > 0 {
		return nil, errors.New(strings.Join(check.Blockers, " "))
	}
	if req.GetDriveId() == "" {
		// Decision 243: a disk not yet adopted.
		drives, err := m.stor.List(ctx)
		if err != nil {
			return nil, err
		}
		var d *maitrev1.Drive
		for _, x := range drives {
			if x.GetDevice() == req.GetDevice() {
				d = x
			}
		}
		if d == nil {
			return nil, fmt.Errorf("no such disk %s", req.GetDevice())
		}
		var out *maitrev1.Drive
		if strings.HasPrefix(d.GetLabel(), storage.ParityLabelPrefix) && d.GetFstype() == "btrfs" {
			if strings.TrimSpace(req.GetConfirm()) != storage.ConfirmWord(d) {
				return nil, fmt.Errorf("repeat exactly: %s", storage.ConfirmWord(d))
			}
			out, err = m.stor.AdoptAs(ctx, req.GetDevice(), req.GetName(), nil, storage.Role{Role: storage.RoleParity})
		} else {
			out, err = m.stor.FormatAs(ctx, req.GetDevice(), req.GetName(), nil, req.GetConfirm(), storage.Role{Role: storage.RoleParity})
		}
		if err != nil {
			return nil, err
		}
		m.kick(ctx)
		return &maitrev1.SetParityDriveResponse{Drive: out}, nil
	}
	var rec maitrev1.DriveRecord
	if _, err := m.st.Get(ctx, storage.KindDrive, req.GetDriveId(), &rec); err != nil {
		return nil, fmt.Errorf("no Drive %s", req.GetDriveId())
	}
	if strings.TrimSpace(req.GetConfirm()) != rec.GetName() {
		return nil, fmt.Errorf("repeat exactly: %s", rec.GetName())
	}
	if len(storage.Leftovers(filepath.Join(m.Mounts, rec.GetDriveId()))) > 0 {
		if m.drains == nil {
			return nil, errors.New("this Node cannot empty Drives")
		}
		job, err := m.drains.Start(ctx, rec.GetDriveId(), req.GetLooseToSpace(), req.GetConfirm(), drain.PurposeParity)
		if err != nil {
			return nil, err
		}
		return &maitrev1.SetParityDriveResponse{Job: job}, nil
	}
	d, err := m.stor.Reformat(ctx, rec.GetDriveId(), storage.Role{Role: storage.RoleParity})
	if err != nil {
		return nil, err
	}
	m.kick(ctx)
	return &maitrev1.SetParityDriveResponse{Drive: d}, nil
}

// afterDrain is the drain manager's Then: a Drive drained to become a
// Parity Drive is formatted and the first sync runs (decision 241).
func (m *Manager) afterDrain(ctx context.Context, job *maitrev1.DrainJob) error {
	if job.GetPurpose() != drain.PurposeParity {
		return nil
	}
	if _, err := m.stor.Reformat(ctx, job.GetDriveId(), storage.Role{Role: storage.RoleParity}); err != nil {
		return err
	}
	m.kick(ctx)
	return nil
}

// unsetParity deletes the parity files and returns the Drive to its
// Spaces (decision 245).
func (m *Manager) unsetParity(ctx context.Context, driveID, confirm string) (*maitrev1.SetParityDriveResponse, error) {
	var rec maitrev1.DriveRecord
	if _, err := m.st.Get(ctx, storage.KindDrive, driveID, &rec); err != nil {
		return nil, fmt.Errorf("no Drive %s", driveID)
	}
	if storage.RoleOf(&rec) != storage.RoleParity {
		return nil, fmt.Errorf("%s is not a Parity Drive", rec.GetName())
	}
	if strings.TrimSpace(confirm) != rec.GetName() {
		return nil, fmt.Errorf("repeat exactly: %s", rec.GetName())
	}
	m.mu.Lock()
	if m.running && m.cancel != nil {
		m.cancel()
	}
	m.mu.Unlock()
	if err := m.stor.SetRole(ctx, driveID, storage.RoleData); err != nil {
		return nil, err
	}
	_ = os.RemoveAll(filepath.Join(m.Mounts, driveID, storage.ParityDir))
	if _, err := m.WriteConfigs(ctx); err != nil {
		m.log.Warn("parity configs", "err", err)
	}
	drives, _ := m.stor.List(ctx)
	for _, d := range drives {
		if d.GetDriveId() == driveID {
			return &maitrev1.SetParityDriveResponse{Drive: d}, nil
		}
	}
	return &maitrev1.SetParityDriveResponse{}, nil
}

// kick rewrites the configs and starts a sync in the background: a new
// Parity Drive builds its parity now.
func (m *Manager) kick(ctx context.Context) {
	go func() {
		if err := m.Sync(context.WithoutCancel(ctx), true); err != nil {
			m.log.Warn("parity sync", "err", err)
		}
	}()
}

// Replace puts a new disk in a failed Drive's place (decision 232): the
// new Drive takes the failed one's name, tags, role and SnapRAID name,
// the failed record points at it, and a rebuild runs in the background.
func (m *Manager) Replace(ctx context.Context, failedID, device, confirm string) (*maitrev1.Drive, error) {
	var old maitrev1.DriveRecord
	if _, err := m.st.Get(ctx, storage.KindDrive, failedID, &old); err != nil {
		return nil, fmt.Errorf("no Drive %s", failedID)
	}
	if old.GetNodeId() != m.self {
		return nil, fmt.Errorf("%s is a Drive of another Node", old.GetName())
	}
	if old.GetReplacedBy() != "" {
		return nil, fmt.Errorf("%s was replaced already", old.GetName())
	}
	if m.Mounted(filepath.Join(m.Mounts, failedID)) {
		return nil, fmt.Errorf("%s is still connected and mounted; a Drive is replaced once it has failed", old.GetName())
	}
	role := storage.Role{Role: old.GetRole(), SnapraidName: Name(&old)}
	d, err := m.stor.FormatAs(ctx, device, old.GetName(), old.GetTags(), confirm, role)
	if err != nil {
		return nil, err
	}
	old.ReplacedBy = d.GetDriveId()
	if _, err := m.st.Put(ctx, storage.KindDrive, failedID, &old, false); err != nil {
		return nil, err
	}
	if m.stor.OnChange != nil {
		m.stor.OnChange()
	}
	go func() {
		bg := context.WithoutCancel(ctx)
		var err error
		if storage.RoleOf(&old) == storage.RoleParity {
			err = m.Sync(bg, true)
		} else {
			err = m.Fix(bg, d.GetDriveId())
		}
		if err != nil {
			m.log.Warn("rebuild", "drive", old.GetName(), "err", err)
		}
	}()
	return d, nil
}

// Status is this Node's parity as the Console sees it.
func (m *Manager) Status(ctx context.Context) *maitrev1.ParityStatus {
	parity, _ := m.ParityDrives(ctx)
	m.mu.Lock()
	defer m.mu.Unlock()
	st := &maitrev1.ParityStatus{
		LastSyncUnixMs: m.state.LastSync, LastScrubUnixMs: m.state.LastScrub, Job: m.job, Progress: m.progress, JobDriveId: m.jobDrive,
		UnprotectedFiles: m.state.Unprotected, Held: m.state.Held, MissingDriveIds: m.state.Missing, Arrays: m.arrays, LastError: m.state.LastError,
	}
	for _, p := range parity {
		st.ParityDriveIds = append(st.ParityDriveIds, p.GetDriveId())
	}
	if len(parity) > 0 {
		if sched, err := m.schedule(ctx); err == nil {
			st.NextSyncUnixMs = NextSync(sched, m.Now()).UnixMilli()
		}
	}
	return st
}

// schedule is this Node's: its own override, else the Fleet's default,
// else nightly at 03:00 with a weekly scrub of 8%.
func (m *Manager) schedule(ctx context.Context) (*maitrev1.ProtectionSchedule, error) {
	var s maitrev1.ProtectionSchedule
	if _, err := m.st.Get(ctx, KindSchedule, m.self, &s); err == nil {
		return &s, nil
	}
	if _, err := m.st.Get(ctx, KindSchedule, ScheduleFleet, &s); err == nil {
		return &s, nil
	}
	return &maitrev1.ProtectionSchedule{}, nil
}

// NextSync is the first scheduled sync after now.
func NextSync(s *maitrev1.ProtectionSchedule, now time.Time) time.Time {
	hh, mm := 3, 0
	if parts := strings.SplitN(s.GetSyncTime(), ":", 2); len(parts) == 2 {
		if h, err := strconv.Atoi(parts[0]); err == nil && h >= 0 && h < 24 {
			hh = h
		}
		if mi, err := strconv.Atoi(parts[1]); err == nil && mi >= 0 && mi < 60 {
			mm = mi
		}
	}
	days := map[int]bool{}
	for _, d := range s.GetSyncDays() {
		days[int(d)%7] = true
	}
	t := time.Date(now.Year(), now.Month(), now.Day(), hh, mm, 0, 0, now.Location())
	for i := 0; i < 8; i++ {
		if t.After(now) && (len(days) == 0 || days[int(t.Weekday())]) {
			return t
		}
		t = t.AddDate(0, 0, 1)
	}
	return t
}

// Schedule runs syncs and scrubs on time until ctx ends, and counts
// unprotected files every hour.
func (m *Manager) Schedule(ctx context.Context) {
	m.load()
	if _, err := m.WriteConfigs(ctx); err != nil {
		m.log.Warn("parity configs", "err", err)
	}
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	hourly := time.NewTicker(time.Hour)
	defer hourly.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-hourly.C:
			m.countUnprotected(ctx)
		case <-tick.C:
			sched, err := m.schedule(ctx)
			if err != nil {
				continue
			}
			now := m.Now()
			m.mu.Lock()
			last := time.UnixMilli(max(m.state.LastAttemptSync, m.state.LastSync))
			m.mu.Unlock()
			// Due when the sync scheduled after the last attempt has passed.
			if due := NextSync(sched, last); !now.Before(due) {
				m.mu.Lock()
				m.state.LastAttemptSync = now.UnixMilli()
				m.mu.Unlock()
				m.save()
				force := sched.GetOverrideOnce() && m.state.Held != ""
				if err := m.Sync(ctx, force); err != nil {
					m.log.Warn("scheduled sync", "err", err)
				}
				every := int(sched.GetScrubEveryDays())
				if every == 0 {
					every = 7
				}
				if m.state.Held == "" && now.Sub(time.UnixMilli(m.state.LastScrub)) >= time.Duration(every)*24*time.Hour {
					if err := m.Scrub(ctx, int(sched.GetScrubPercent())); err != nil {
						m.log.Warn("scheduled scrub", "err", err)
					}
				}
			}
		}
	}
}

// ErrBusy says a sync, scrub or rebuild is running already.
var ErrBusy = errors.New("parity is busy: a sync, scrub or rebuild is running")

func (m *Manager) begin(job, drive string) (context.Context, func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running {
		return nil, nil, ErrBusy
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.running, m.job, m.jobDrive, m.progress, m.cancel = true, job, drive, 0, cancel
	return ctx, func() {
		cancel()
		m.mu.Lock()
		m.running, m.job, m.jobDrive, m.progress, m.cancel = false, "", "", 0, nil
		m.mu.Unlock()
	}, nil
}

// Sync updates every array's parity. Unless forced, it first checks each
// array (decision 231) and holds the sync, with the reason in the status,
// when a Drive is missing or empty or too many files were deleted.
func (m *Manager) Sync(ctx context.Context, force bool) error {
	arrays, err := m.WriteConfigs(ctx)
	if err != nil {
		return err
	}
	if len(arrays) == 0 {
		return nil
	}
	jctx, end, err := m.begin("sync", "")
	if err != nil {
		return err
	}
	defer end()
	ctx = joined(ctx, jctx)
	var missing []string
	for _, a := range arrays {
		for _, d := range append(append([]*maitrev1.DriveRecord(nil), a.Drives...), a.Parity[:min(a.LevelGot, len(a.Parity))]...) {
			dir := filepath.Join(m.Mounts, d.GetDriveId())
			if !m.Mounted(dir) {
				missing = appendOnce(missing, d.GetDriveId())
			}
		}
	}
	m.mu.Lock()
	m.state.Missing = missing
	m.mu.Unlock()
	if len(missing) > 0 {
		return m.hold(fmt.Sprintf("%s not connected. A sync now would record its files as deleted.", m.names(ctx, missing)))
	}
	if !force {
		for _, a := range arrays {
			if why := m.guard(ctx, a); why != "" {
				return m.hold(why)
			}
		}
	}
	for _, a := range arrays {
		if err := m.snapraid(ctx, a.Level, "sync"); err != nil {
			m.mu.Lock()
			m.state.LastError = fmt.Sprintf("sync of level %d: %v", a.Level, err)
			m.mu.Unlock()
			m.save()
			return err
		}
	}
	m.mu.Lock()
	m.state.LastSync, m.state.Held, m.state.Unprotected, m.state.LastError = m.Now().UnixMilli(), "", 0, ""
	m.mu.Unlock()
	m.save()
	return nil
}

func (m *Manager) hold(why string) error {
	m.mu.Lock()
	m.state.Held = why
	m.mu.Unlock()
	m.save()
	m.log.Warn("parity sync held", "why", why)
	return errors.New(why)
}

// guard checks one array before an automatic sync: a data Drive whose
// included folders are all gone (mounted, but empty: a filesystem that
// came back blank), or deletions above the threshold.
func (m *Manager) guard(ctx context.Context, a Array) string {
	for _, d := range a.Drives {
		dir := filepath.Join(m.Mounts, d.GetDriveId())
		content := filepath.Join(dir, storage.ParityDir, fmt.Sprintf("level%d.content", a.Level))
		if _, err := os.Stat(content); err != nil {
			continue // never synced: nothing to lose
		}
		any := false
		for _, s := range a.Spaces {
			if storage.HasFiles(filepath.Join(dir, s.GetSpaceId())) {
				any = true
			}
		}
		if !any {
			return fmt.Sprintf("%s looks empty: none of its protected folders have files, though the last sync saw some. If the Drive was wiped, its files can still be rebuilt from parity; a sync now would forget them.", d.GetName())
		}
	}
	diff, err := m.diff(ctx, a.Level)
	if err != nil {
		return ""
	}
	total := diff.equal + diff.removed + diff.updated + diff.moved + diff.copied + diff.restored
	if total > 0 && float64(diff.removed) > HeldDeletedShare*float64(total) {
		return fmt.Sprintf("%d of %d protected files (%.0f%%) were deleted since the last sync. If that was not you, they can still be restored; a sync now makes it final.", diff.removed, total, 100*float64(diff.removed)/float64(total))
	}
	return ""
}

type diffCounts struct{ equal, added, removed, updated, moved, copied, restored int }

var diffRe = regexp.MustCompile(`^\s*(\d+)\s+(equal|added|removed|updated|moved|copied|restored)\b`)

// diff runs `snapraid diff` and reads its summary.
func (m *Manager) diff(ctx context.Context, level int) (diffCounts, error) {
	var c diffCounts
	out, err := m.Run.Run(ctx, "snapraid", "-c", m.configPath(level), "diff")
	// Exit status 2 means "there are differences"; the summary is printed anyway.
	for _, line := range strings.Split(out, "\n") {
		mm := diffRe.FindStringSubmatch(line)
		if mm == nil {
			continue
		}
		n, _ := strconv.Atoi(mm[1])
		switch mm[2] {
		case "equal":
			c.equal = n
		case "added":
			c.added = n
		case "removed":
			c.removed = n
		case "updated":
			c.updated = n
		case "moved":
			c.moved = n
		case "copied":
			c.copied = n
		case "restored":
			c.restored = n
		}
	}
	if err != nil && c.equal == 0 && c.added == 0 && c.removed == 0 && c.updated == 0 {
		return c, err
	}
	return c, nil
}

// countUnprotected sums the files each array would change at the next sync.
func (m *Manager) countUnprotected(ctx context.Context) {
	m.mu.Lock()
	busy := m.running
	m.mu.Unlock()
	if busy {
		return
	}
	arrays, err := m.Arrays(ctx)
	if err != nil {
		return
	}
	var n int
	for _, a := range arrays {
		if d, err := m.diff(ctx, a.Level); err == nil {
			n += d.added + d.removed + d.updated
		}
	}
	m.mu.Lock()
	m.state.Unprotected = uint32(n)
	m.mu.Unlock()
}

// Scrub checks a share of each array's data against its parity.
func (m *Manager) Scrub(ctx context.Context, percent int) error {
	arrays, err := m.Arrays(ctx)
	if err != nil || len(arrays) == 0 {
		return err
	}
	if percent <= 0 {
		percent = 8
	}
	jctx, end, err := m.begin("scrub", "")
	if err != nil {
		return err
	}
	defer end()
	ctx = joined(ctx, jctx)
	for _, a := range arrays {
		if err := m.snapraid(ctx, a.Level, "scrub", "-p", strconv.Itoa(percent), "-o", "10"); err != nil {
			m.mu.Lock()
			m.state.LastError = fmt.Sprintf("scrub of level %d: %v", a.Level, err)
			m.mu.Unlock()
			m.save()
			return err
		}
	}
	m.mu.Lock()
	m.state.LastScrub = m.Now().UnixMilli()
	m.mu.Unlock()
	m.save()
	return nil
}

// Fix rebuilds a replaced Drive's files from parity in every array it is in.
func (m *Manager) Fix(ctx context.Context, driveID string) error {
	arrays, err := m.WriteConfigs(ctx)
	if err != nil {
		return err
	}
	var rec maitrev1.DriveRecord
	if _, err := m.st.Get(ctx, storage.KindDrive, driveID, &rec); err != nil {
		return fmt.Errorf("no Drive %s", driveID)
	}
	jctx, end, err := m.begin("fix", driveID)
	if err != nil {
		return err
	}
	defer end()
	ctx = joined(ctx, jctx)
	for _, a := range arrays {
		in := false
		for _, d := range a.Drives {
			in = in || d.GetDriveId() == driveID
		}
		if !in {
			continue
		}
		if err := m.snapraid(ctx, a.Level, "fix", "-d", Name(&rec)); err != nil {
			m.mu.Lock()
			m.state.LastError = fmt.Sprintf("rebuild of %s: %v", rec.GetName(), err)
			m.mu.Unlock()
			m.save()
			return err
		}
	}
	m.mu.Lock()
	m.state.LastError = ""
	m.mu.Unlock()
	m.save()
	return m.Sync(ctx, true)
}

var percentRe = regexp.MustCompile(`^\s*(\d+)%`)

// snapraid runs one command on one array at idle priority, following its
// progress lines.
func (m *Manager) snapraid(ctx context.Context, level int, verb string, extra ...string) error {
	args := []string{"-c", m.configPath(level), verb}
	args = append(args, extra...)
	if _, ok := m.Run.(storage.ExecRunner); !ok {
		// Tests: the fake runner sees the same arguments, no progress.
		_, err := m.Run.Run(ctx, "snapraid", args...)
		return err
	}
	name := "snapraid"
	if m.Nice {
		args = append([]string{"-c", "3", "nice", "-n", "19", "snapraid"}, args...)
		name = "ionice"
	}
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return err
	}
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	sc.Split(scanLinesOrCR)
	var tail []string
	for sc.Scan() {
		line := sc.Text()
		if mm := percentRe.FindStringSubmatch(line); mm != nil {
			p, _ := strconv.Atoi(mm[1])
			m.mu.Lock()
			m.progress = float64(p) / 100
			m.mu.Unlock()
			continue
		}
		if t := strings.TrimSpace(line); t != "" {
			tail = append(tail, t)
			if len(tail) > 5 {
				tail = tail[1:]
			}
		}
	}
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("snapraid %s: %w: %s", verb, err, strings.Join(tail, " / "))
	}
	return nil
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

// joined ends when either context does.
func joined(a, b context.Context) context.Context {
	ctx, cancel := context.WithCancel(a)
	go func() {
		select {
		case <-b.Done():
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx
}

func (m *Manager) names(ctx context.Context, ids []string) string {
	var out []string
	for _, id := range ids {
		var rec maitrev1.DriveRecord
		if _, err := m.st.Get(ctx, storage.KindDrive, id, &rec); err == nil {
			out = append(out, rec.GetName())
		} else {
			out = append(out, id)
		}
	}
	switch len(out) {
	case 1:
		return out[0] + " is"
	default:
		return strings.Join(out, ", ") + " are"
	}
}

func appendOnce(xs []string, x string) []string {
	if contains(xs, x) {
		return xs
	}
	return append(xs, x)
}

func human(b uint64) string {
	switch {
	case b >= 1<<40:
		return fmt.Sprintf("%.1f TB", float64(b)/float64(1<<40))
	case b >= 1<<30:
		return fmt.Sprintf("%.0f GB", float64(b)/float64(1<<30))
	default:
		return fmt.Sprintf("%d MB", b>>20)
	}
}

func (m *Manager) save() {
	if m.StateFile == "" {
		return
	}
	m.mu.Lock()
	data, _ := json.Marshal(m.state)
	m.mu.Unlock()
	tmp := m.StateFile + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err == nil {
		_ = os.Rename(tmp, m.StateFile)
	}
}

func (m *Manager) load() {
	if m.StateFile == "" {
		return
	}
	data, err := os.ReadFile(m.StateFile)
	if err != nil {
		return
	}
	m.mu.Lock()
	_ = json.Unmarshal(data, &m.state)
	m.mu.Unlock()
}
