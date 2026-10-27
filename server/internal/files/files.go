// Package files is the file manager's back end (decision 122): browse a
// Space's Pool as one tree or a Drive's root, say where each entry lives and
// how big it is, and move things between Spaces, Drives and Nodes. Moves
// copy with rsync, then remove the source; a failed move leaves the source
// untouched and says what was left at the destination.
package files

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"google.golang.org/protobuf/proto"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1/maitrev1connect"
	"github.com/Onfire7/maitre-de-serveur/server/internal/pool"
	"github.com/Onfire7/maitre-de-serveur/server/internal/storage"
	"github.com/Onfire7/maitre-de-serveur/server/internal/store"
)

// mountedForTest, when set, stands in for the mount table.
var mountedForTest map[string]bool

func isMounted(dir string) bool {
	if mountedForTest != nil {
		return mountedForTest[dir]
	}
	return pool.Mounted(dir)
}

// Manager answers FilesService on one Node.
type Manager struct {
	st     *store.Store
	log    *slog.Logger
	run    storage.Runner
	self   string
	Mounts string // Drives: Mounts/<drive_id>
	Pools  string // Pools: Pools/<space_id>
	Remote string // peers' Pools: Remote/<node_id>/<space_id>
	// PeerAddr returns an address of a peer Node ("" if unknown), and
	// PeerHTTP the mTLS client to call it with; nil on a single-Node setup.
	PeerAddr func(nodeID string) string
	PeerHTTP func() *http.Client
	// Rsync is the rsync binary; "rsync" by default.
	Rsync string

	mu     sync.Mutex
	usage  map[string]*usageEntry
	jobs   map[string]*job
	order  []string
	queue  chan *job
	seq    int
	cancel map[string]context.CancelFunc
}

type usageEntry struct {
	bytes, files uint64
	done         bool
	at           time.Time
}

type job struct {
	mu  sync.Mutex
	pb  *maitrev1.Job
	ctx context.Context
	// done runs once the job finished, with nil on success, the error on
	// failure, or context.Canceled when cancelled.
	done func(error)
	// run does the work of a job that is not a move (Zip, Unzip).
	run func(ctx context.Context, j *job) error
	// keepOwner: a Service's own volume moving (MoveThen) keeps the owner
	// its container gave it; file manager moves take the destination
	// Space's owner (decision 173).
	keepOwner bool
}

// New builds a Manager. self is this Node's id.
func New(st *store.Store, log *slog.Logger, self string) *Manager {
	m := &Manager{
		st: st, log: log, run: storage.ExecRunner{}, self: self,
		Mounts: pool.DrivesDir, Pools: pool.PoolsDir, Remote: storage.RemoteDir, Rsync: "rsync",
		usage: map[string]*usageEntry{}, jobs: map[string]*job{}, queue: make(chan *job, 64), cancel: map[string]context.CancelFunc{},
	}
	return m
}

// Run executes queued moves one at a time until ctx ends.
func (m *Manager) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case j := <-m.queue:
			m.execute(ctx, j)
		}
	}
}

var cleanRe = regexp.MustCompile(`/+`)

// rel cleans a user path to a safe relative path ("" for the top).
func rel(p string) (string, error) {
	p = cleanRe.ReplaceAllString(strings.TrimSpace(p), "/")
	p = strings.Trim(p, "/")
	if p == "" {
		return "", nil
	}
	c := filepath.Clean(p)
	if c == "." || c == ".." || strings.HasPrefix(c, "../") || strings.HasPrefix(c, "/") {
		return "", errors.New("bad path")
	}
	return c, nil
}

// base resolves a scope to its directory on this Node.
func (m *Manager) base(ctx context.Context, sc *maitrev1.Scope) (string, error) {
	switch {
	case sc.GetSpaceId() != "" && sc.GetDriveId() == "":
		dir := filepath.Join(m.Pools, sc.GetSpaceId())
		if !isMounted(dir) {
			return "", fmt.Errorf("Space %s has no Pool on this Node", sc.GetSpaceId())
		}
		return dir, nil
	case sc.GetDriveId() != "" && sc.GetSpaceId() == "":
		var rec maitrev1.DriveRecord
		if _, err := m.st.Get(ctx, storage.KindDrive, sc.GetDriveId(), &rec); err != nil || rec.GetNodeId() != m.self {
			return "", fmt.Errorf("Drive %s is not on this Node", sc.GetDriveId())
		}
		dir := filepath.Join(m.Mounts, sc.GetDriveId())
		if !isMounted(dir) {
			return "", fmt.Errorf("Drive %s is not mounted", rec.GetName())
		}
		return dir, nil
	}
	return "", errors.New("scope needs exactly one of space_id or drive_id")
}

// ListDir lists one directory of a scope.
func (m *Manager) ListDir(ctx context.Context, sc *maitrev1.Scope, path string) ([]*maitrev1.Entry, error) {
	base, err := m.base(ctx, sc)
	if err != nil {
		return nil, err
	}
	r, err := rel(path)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(base, r)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []*maitrev1.Entry
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || (r == "" && name == "lost+found") {
			// Maître's own markers, the Node's config backup, and the
			// filesystem's lost+found are not the user's files.
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		en := &maitrev1.Entry{Name: name, Dir: e.IsDir(), ModifiedUnixMs: info.ModTime().UnixMilli()}
		if !e.IsDir() {
			en.SizeBytes = uint64(info.Size())
		}
		if sc.GetSpaceId() != "" {
			en.Locations = m.locations(filepath.Join(dir, name))
		} else {
			en.Locations = []*maitrev1.Location{{NodeId: m.self, DriveId: sc.GetDriveId()}}
		}
		out = append(out, en)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Dir != out[j].Dir {
			return out[i].Dir
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

// ErrExists is returned by MakeDir when the name is taken.
var ErrExists = errors.New("already exists")

// MakeDir makes one folder at path; its parent must exist. Names starting
// with a dot are refused: ListDir hides them, so the folder would vanish.
func (m *Manager) MakeDir(ctx context.Context, sc *maitrev1.Scope, path string) error {
	base, err := m.base(ctx, sc)
	if err != nil {
		return err
	}
	r, err := rel(path)
	if err != nil {
		return err
	}
	if r == "" {
		return errors.New("name the new folder")
	}
	if name := filepath.Base(r); strings.HasPrefix(name, ".") {
		return fmt.Errorf("%q: folder names cannot start with a dot", name)
	}
	if err := os.Mkdir(filepath.Join(base, r), 0o775); err == nil {
		m.own(filepath.Join(base, r))
	} else {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("%s %w", r, ErrExists)
		}
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("%s: the folder it goes in does not exist", r)
		}
		return err
	}
	return nil
}

// locations asks mergerfs which branches hold an entry (xattr
// user.mergerfs.allpaths, NUL-separated) and maps each branch to a Node
// and Drive.
func (m *Manager) locations(p string) []*maitrev1.Location {
	paths := allPaths(p)
	if len(paths) == 0 {
		return nil
	}
	var out []*maitrev1.Location
	for _, full := range paths {
		if l := m.locate(full); l != nil {
			out = append(out, l)
		}
	}
	return out
}

// locate maps a branch path to its Node and Drive.
func (m *Manager) locate(full string) *maitrev1.Location {
	if r, err := filepath.Rel(m.Mounts, full); err == nil && !strings.HasPrefix(r, "..") {
		parts := strings.SplitN(r, string(filepath.Separator), 2)
		return &maitrev1.Location{NodeId: m.self, DriveId: parts[0]}
	}
	if r, err := filepath.Rel(m.Remote, full); err == nil && !strings.HasPrefix(r, "..") {
		parts := strings.SplitN(r, string(filepath.Separator), 2)
		return &maitrev1.Location{NodeId: parts[0]}
	}
	return nil
}

// Usage returns the size of a tree; the first call starts the walk and
// reports in_progress, later calls return the cached answer (a minute).
func (m *Manager) Usage(ctx context.Context, sc *maitrev1.Scope, path string) (*maitrev1.UsageResponse, error) {
	base, err := m.base(ctx, sc)
	if err != nil {
		return nil, err
	}
	r, err := rel(path)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(base, r)
	m.mu.Lock()
	u := m.usage[dir]
	if u != nil && (!u.done || time.Since(u.at) < time.Minute) {
		m.mu.Unlock()
		return &maitrev1.UsageResponse{Bytes: u.bytes, Files: u.files, InProgress: !u.done}, nil
	}
	u = &usageEntry{at: time.Now()}
	m.usage[dir] = u
	m.mu.Unlock()
	go func() {
		bytes, files := walk(dir)
		m.mu.Lock()
		u.bytes, u.files, u.done, u.at = bytes, files, true, time.Now()
		m.mu.Unlock()
	}()
	return &maitrev1.UsageResponse{InProgress: true}, nil
}

func walk(dir string) (bytes, files uint64) {
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			bytes += uint64(info.Size())
			files++
		}
		return nil
	})
	return bytes, files
}

// Move queues a move and returns its job.
func (m *Manager) Move(ctx context.Context, req *maitrev1.MoveRequest) (*maitrev1.Job, error) {
	return m.MoveThen(ctx, req, nil)
}

// MoveThen queues a move and runs done when it has finished (nil, the
// error, or context.Canceled). Used by Service volume moves.
func (m *Manager) MoveThen(ctx context.Context, req *maitrev1.MoveRequest, done func(error)) (*maitrev1.Job, error) {
	if _, err := rel(req.GetPath()); err != nil {
		return nil, err
	}
	if req.GetPath() == "" || req.GetPath() == "/" {
		return nil, errors.New("pick a file or folder, not the top")
	}
	if req.GetToSpace() == "" {
		return nil, errors.New("a destination Space is required")
	}
	if req.GetToPath() == "" {
		req.ToPath = req.GetPath()
	}
	if _, err := rel(req.GetToPath()); err != nil {
		return nil, err
	}
	spaces, err := storage.Spaces(ctx, m.st)
	if err != nil {
		return nil, err
	}
	var dest *maitrev1.Space
	for _, b := range spaces {
		if b.GetSpaceId() == req.GetToSpace() {
			dest = b
		}
	}
	if dest == nil {
		return nil, fmt.Errorf("no Space %s", req.GetToSpace())
	}
	if req.GetToDriveId() != "" {
		var rec maitrev1.DriveRecord
		if _, err := m.st.Get(ctx, storage.KindDrive, req.GetToDriveId(), &rec); err != nil {
			return nil, fmt.Errorf("no Drive %s", req.GetToDriveId())
		}
		member := false
		for _, id := range storage.SpacesOf(&rec, spaces) {
			member = member || id == req.GetToSpace()
		}
		if !member {
			return nil, fmt.Errorf("Drive %s does not serve Space %s", rec.GetName(), dest.GetName())
		}
		req.ToNodeId = rec.GetNodeId()
	}
	if req.GetFromNodeId() == "" {
		// Our own source: it must resolve here.
		if _, err := m.base(ctx, req.GetFrom()); err != nil {
			return nil, err
		}
	} else if req.GetFrom().GetSpaceId() == "" {
		return nil, errors.New("a move from another Node must come from a Space")
	}
	if req.GetToNodeId() != "" && req.GetToNodeId() != m.self && req.GetFrom().GetDriveId() != "" {
		return nil, errors.New("files on a Drive move to a Space on this Node first; then anywhere")
	}
	return m.enqueue(&job{pb: &maitrev1.Job{State: "queued", Request: req}, done: done, keepOwner: done != nil}), nil
}

// enqueue gives j an id and queues it behind the other jobs.
func (m *Manager) enqueue(j *job) *maitrev1.Job {
	m.mu.Lock()
	m.seq++
	j.pb.JobId = fmt.Sprintf("job-%d-%d", time.Now().Unix(), m.seq)
	m.jobs[j.pb.JobId] = j
	m.order = append(m.order, j.pb.JobId)
	m.mu.Unlock()
	select {
	case m.queue <- j:
	default:
		m.setState(j, "failed", "too many queued jobs")
	}
	return m.snapshot(j)
}

// Jobs lists every job, newest last.
func (m *Manager) Jobs() []*maitrev1.Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*maitrev1.Job
	for _, id := range m.order {
		out = append(out, m.snapshot(m.jobs[id]))
	}
	return out
}

// Cancel stops a queued or running job.
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
		return nil
	}
	if c != nil {
		c()
	}
	return nil
}

func (m *Manager) snapshot(j *job) *maitrev1.Job {
	j.mu.Lock()
	defer j.mu.Unlock()
	return proto.Clone(j.pb).(*maitrev1.Job)
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

// execute performs one move.
func (m *Manager) execute(parent context.Context, j *job) {
	j.mu.Lock()
	if j.pb.State != "queued" {
		j.mu.Unlock()
		return
	}
	req := j.pb.Request
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
	var err error
	if j.run != nil {
		err = j.run(ctx, j)
	} else {
		err = m.move(ctx, j, req)
	}
	untouched := "; the source is untouched"
	if j.run != nil {
		untouched = "; the originals are untouched"
	}
	switch {
	case err == nil:
		m.setState(j, "done", "")
	case ctx.Err() != nil:
		m.setState(j, "cancelled", "stopped"+untouched+", a partial result may remain")
		err = context.Canceled
	default:
		m.setState(j, "failed", err.Error()+untouched)
		m.log.Warn("job failed", "job", j.pb.JobId, "kind", j.pb.Kind, "err", err)
	}
	if j.done != nil {
		j.done(err)
	}
}

func (m *Manager) move(ctx context.Context, j *job, req *maitrev1.MoveRequest) error {
	if req.GetToNodeId() != "" && req.GetToNodeId() != m.self {
		return m.forward(ctx, j, req)
	}
	// Source.
	var src string
	if req.GetFromNodeId() != "" && req.GetFromNodeId() != m.self {
		if m.PeerAddr == nil {
			return errors.New("no peers")
		}
		if m.PeerAddr(req.GetFromNodeId()) == "" {
			return fmt.Errorf("no address for Node %s", req.GetFromNodeId())
		}
		dir, err := storage.MountRemotePool(ctx, m.run, req.GetFromNodeId(), req.GetFrom().GetSpaceId())
		if err != nil {
			return fmt.Errorf("reach the source Pool: %w", err)
		}
		src = filepath.Join(dir, req.GetPath())
	} else {
		base, err := m.base(ctx, req.GetFrom())
		if err != nil {
			return err
		}
		src = filepath.Join(base, req.GetPath())
	}
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	// Destination.
	var dstBase string
	if req.GetToDriveId() != "" {
		dstBase = filepath.Join(m.Mounts, req.GetToDriveId(), req.GetToSpace())
		if !isMounted(filepath.Join(m.Mounts, req.GetToDriveId())) {
			return errors.New("the destination Drive is not mounted")
		}
	} else {
		dstBase = filepath.Join(m.Pools, req.GetToSpace())
		if !isMounted(dstBase) {
			return fmt.Errorf("Space %s has no Pool on this Node", req.GetToSpace())
		}
	}
	dst := filepath.Join(dstBase, req.GetToPath())
	if same, _ := samePath(src, dst); same {
		return errors.New("source and destination are the same place")
	}
	if strings.HasPrefix(dst+"/", src+"/") {
		return errors.New("cannot move a folder into itself")
	}
	if err := m.mkdirs(dstBase, filepath.Dir(req.GetToPath())); err != nil {
		return err
	}
	bytes, _ := walk(src)
	if !info.IsDir() {
		bytes = uint64(info.Size())
	}
	j.mu.Lock()
	j.pb.BytesTotal = bytes
	j.mu.Unlock()
	// Between folders of one Space on this Node, to a name not taken: a
	// rename through the Pool, instant when mergerfs keeps it on one Drive.
	// EXDEV (the Pool would have to cross Drives) falls back to the copy.
	if req.GetFromNodeId() == "" && req.GetToDriveId() == "" && req.GetFrom().GetSpaceId() != "" && req.GetFrom().GetSpaceId() == req.GetToSpace() {
		if _, err := os.Lstat(dst); errors.Is(err, fs.ErrNotExist) {
			err := os.Rename(src, dst)
			if err == nil {
				j.mu.Lock()
				j.pb.BytesDone = bytes
				j.mu.Unlock()
				return nil
			}
			if !errors.Is(err, syscall.EXDEV) {
				return err
			}
		}
	}
	// Same filesystem and not across the Pool: a rename is instant. Only
	// for Drive-to-Drive within one Drive; everything else copies.
	if err := m.rsync(ctx, j, src, dst, info.IsDir()); err != nil {
		return err
	}
	if !j.keepOwner && req.GetFrom().GetSpaceId() != req.GetToSpace() {
		m.ownTree(dst)
	}
	// Copied and checked (rsync -c on the second pass); remove the source.
	// Through a Pool, removal takes every branch's copy.
	if req.GetFromNodeId() != "" && req.GetFromNodeId() != m.self {
		return nil // the forwarding Node removes its own source
	}
	return os.RemoveAll(src)
}

func samePath(a, b string) (bool, error) {
	ia, err := os.Stat(a)
	if err != nil {
		return false, err
	}
	ib, err := os.Stat(b)
	if err != nil {
		return false, nil
	}
	return os.SameFile(ia, ib), nil
}

var progressRe = regexp.MustCompile(`^\s*([\d,]+)\s+\d+%`)

// rsync copies src to dst with progress, then verifies with a checksum pass.
func (m *Manager) rsync(ctx context.Context, j *job, src, dst string, dir bool) error {
	s, d := src, dst
	if dir {
		s, d = src+"/", dst+"/"
	}
	run := func(verify bool, extra ...string) error {
		args := append([]string{"-a", "--info=progress2", "--no-inc-recursive"}, extra...)
		args = append(args, s, d)
		cmd := exec.CommandContext(ctx, m.Rsync, args...)
		cmd.SysProcAttr = sysProcAttr()
		out, err := cmd.StdoutPipe()
		if err != nil {
			return err
		}
		cmd.Stderr = nil
		if err := cmd.Start(); err != nil {
			return err
		}
		sc := bufio.NewScanner(out)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		sc.Split(scanLinesOrCR)
		for sc.Scan() {
			line := sc.Text()
			if mm := progressRe.FindStringSubmatch(line); mm != nil {
				n, _ := strconv.ParseUint(strings.ReplaceAll(mm[1], ",", ""), 10, 64)
				j.mu.Lock()
				if !verify {
					j.pb.BytesDone = n
				}
				j.mu.Unlock()
			} else if t := strings.TrimSpace(line); t != "" && !strings.HasPrefix(t, "sending") && !strings.HasPrefix(t, "sent ") && !strings.HasPrefix(t, "total ") {
				j.mu.Lock()
				j.pb.Current = t
				j.mu.Unlock()
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
	if err := run(false); err != nil {
		return err
	}
	// Second pass with checksums: confirms every byte arrived. It transfers
	// nothing, so it does not touch the byte count.
	if err := run(true, "-c"); err != nil {
		return err
	}
	j.mu.Lock()
	j.pb.BytesDone, j.pb.Current = j.pb.BytesTotal, ""
	j.mu.Unlock()
	return nil
}

// scanLinesOrCR splits on \n or \r: rsync redraws progress with \r.
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

// forward hands a move to the destination Node: it pulls from our Pool
// over NFS; when its job finishes, we remove the source.
func (m *Manager) forward(ctx context.Context, j *job, req *maitrev1.MoveRequest) error {
	if m.PeerAddr == nil || m.PeerHTTP == nil {
		return errors.New("no peers")
	}
	addr := m.PeerAddr(req.GetToNodeId())
	if addr == "" {
		return fmt.Errorf("no address for Node %s", req.GetToNodeId())
	}
	base, err := m.base(ctx, req.GetFrom())
	if err != nil {
		return err
	}
	src := filepath.Join(base, req.GetPath())
	if _, err := os.Lstat(src); err != nil {
		return err
	}
	c := maitrev1connect.NewFilesServiceClient(connect.NewClient(connecthttp.NewTransport(m.PeerHTTP(), "https://"+addr)))
	fwd := &maitrev1.MoveRequest{From: req.GetFrom(), Path: req.GetPath(), ToSpace: req.GetToSpace(), ToPath: req.GetToPath(), ToNodeId: req.GetToNodeId(), ToDriveId: req.GetToDriveId(), FromNodeId: m.self}
	res, err := c.Move(ctx, fwd)
	if err != nil {
		return fmt.Errorf("ask %s: %w", req.GetToNodeId(), err)
	}
	remote := res.GetJobId()
	for {
		select {
		case <-ctx.Done():
			_, _ = c.CancelJob(context.Background(), &maitrev1.CancelJobRequest{JobId: remote})
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
		lr, err := c.ListJobs(ctx, &maitrev1.ListJobsRequest{})
		if err != nil {
			continue
		}
		for _, rj := range lr.GetJobs() {
			if rj.GetJobId() != remote {
				continue
			}
			j.mu.Lock()
			j.pb.BytesDone, j.pb.BytesTotal, j.pb.Current = rj.GetBytesDone(), rj.GetBytesTotal(), rj.GetCurrent()
			j.mu.Unlock()
			switch rj.GetState() {
			case "done":
				return os.RemoveAll(src)
			case "failed", "cancelled":
				return fmt.Errorf("%s: %s", req.GetToNodeId(), rj.GetMessage())
			}
		}
	}
}
