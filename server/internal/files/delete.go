package files

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/store"
)

// Delete queues a job that removes items of one folder for good (decision
// 186). Through a Pool, every Drive's copy goes.
func (m *Manager) Delete(ctx context.Context, req *maitrev1.DeleteRequest) (*maitrev1.Job, error) {
	base, err := m.base(ctx, req.GetScope())
	if err != nil {
		return nil, err
	}
	folder, err := rel(req.GetFolder())
	if err != nil {
		return nil, err
	}
	if len(req.GetNames()) == 0 {
		return nil, errors.New("pick something to delete")
	}
	used, err := m.usedFolders(ctx)
	if err != nil {
		return nil, err
	}
	var total uint64
	var paths []string
	for _, n := range req.GetNames() {
		if n == "" || strings.ContainsRune(n, '/') || n == "." || n == ".." {
			return nil, fmt.Errorf("%q is not a name in the folder", n)
		}
		r := filepath.Join(folder, n)
		info, err := os.Lstat(filepath.Join(base, r))
		if err != nil {
			return nil, fmt.Errorf("%s: not found", n)
		}
		space, inSpace := req.GetScope().GetSpaceId(), r
		if d := req.GetScope().GetDriveId(); d != "" {
			// On a Drive, <space>/... is that Space's branch; the folder
			// itself belongs to the Pool, not to the user.
			parts := strings.SplitN(r, "/", 2)
			if m.isSpace(ctx, parts[0]) {
				if len(parts) == 1 {
					return nil, fmt.Errorf("%s is this Drive's part of Space %s; remove the Drive from the Space instead", n, parts[0])
				}
				space, inSpace = parts[0], parts[1]
			} else {
				space = ""
			}
		}
		if space != "" {
			for _, u := range used[space] {
				if u.folder == inSpace || strings.HasPrefix(u.folder, inSpace+"/") {
					return nil, fmt.Errorf("%s uses %s; change its folders first", u.service, strings.TrimSuffix(filepath.Join(space, u.folder), "/"))
				}
			}
		}
		if info.IsDir() {
			b, _ := walk(filepath.Join(base, r))
			total += b
		} else {
			total += uint64(info.Size())
		}
		paths = append(paths, filepath.Join(base, r))
	}
	what := fmt.Sprintf("%d items", len(paths))
	if len(paths) == 1 {
		what = req.GetNames()[0]
	}
	j := &job{pb: &maitrev1.Job{State: "queued", Kind: "delete", Summary: "Delete " + what, BytesTotal: total, Current: folder}}
	j.run = func(ctx context.Context, j *job) error { return m.remove(ctx, j, paths) }
	return m.enqueue(j), nil
}

func (m *Manager) remove(ctx context.Context, j *job, paths []string) error {
	var done uint64
	for _, p := range paths {
		// Files first, deepest folders last, so progress moves and a
		// cancel leaves whole folders where it can.
		var dirs []string
		err := filepath.WalkDir(p, func(q string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if d.IsDir() {
				dirs = append(dirs, q)
				return nil
			}
			var size int64
			if info, err := d.Info(); err == nil && info.Mode().IsRegular() {
				size = info.Size()
			}
			if err := os.Remove(q); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			done += uint64(size)
			j.mu.Lock()
			j.pb.BytesDone, j.pb.Current = done, q
			j.mu.Unlock()
			return nil
		})
		if err != nil {
			return err
		}
		sort.Sort(sort.Reverse(sort.StringSlice(dirs)))
		for _, d := range dirs {
			if err := os.Remove(d); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}
	}
	return nil
}

type usedFolder struct{ service, folder string }

// usedFolders lists, per Space, the folders Services use (relative to the
// Space's top): own volumes, picked folders and extra folders.
func (m *Manager) usedFolders(ctx context.Context) (map[string][]usedFolder, error) {
	specs, err := store.ListLive(ctx, m.st, "service", func() *maitrev1.ServiceSpec { return &maitrev1.ServiceSpec{} })
	if err != nil {
		return nil, err
	}
	out := map[string][]usedFolder{}
	add := func(space, svc, folder string) {
		out[space] = append(out[space], usedFolder{svc, strings.Trim(filepath.Clean("/"+folder), "/")})
	}
	for _, s := range specs {
		for _, v := range s.GetVolumes() {
			space := s.GetVolumeSpaces()[v.GetName()]
			if f, ok := s.GetVolumeFolders()[v.GetName()]; ok {
				add(space, s.GetName(), f)
			} else {
				add(space, s.GetName(), s.GetName()+"/"+v.GetName())
			}
		}
		for _, mt := range s.GetMounts() {
			add(mt.GetSpaceId(), s.GetName(), mt.GetFolder())
		}
	}
	return out, nil
}
