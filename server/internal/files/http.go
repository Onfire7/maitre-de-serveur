package files

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/owner"
)

// ContentPath is where file contents are read and written (decision 183).
const ContentPath = "/maitre/files/content"

// Handler serves ContentPath. GET sends a file with ranges (download=1 makes
// it an attachment) or, with zip=1, a folder as a zip made on the fly. PUT
// writes a new file from the body (mkdirs=1 makes missing folders) and never
// replaces one. allow decides whether the caller may use it at all.
func (m *Manager) Handler(allow func(*http.Request) bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allow(r) {
			http.Error(w, "not allowed", http.StatusForbidden)
			return
		}
		q := r.URL.Query()
		sc := &maitrev1.Scope{SpaceId: q.Get("space"), DriveId: q.Get("drive")}
		base, err := m.base(r.Context(), sc)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		p, err := rel(q.Get("path"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		full := filepath.Join(base, p)
		switch r.Method {
		case http.MethodGet, http.MethodHead:
			m.serveGet(w, r, full, p)
		case http.MethodPut:
			// Decision 189: an upload writes as the Space's owner.
			_ = owner.As(m.ownerOf(sc.GetSpaceId()), func() error {
				m.servePut(w, r, base, p, q.Get("mkdirs") == "1")
				return nil
			})
		default:
			w.Header().Set("Allow", "GET, HEAD, PUT")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
}

func (m *Manager) serveGet(w http.ResponseWriter, r *http.Request, full, p string) {
	info, err := os.Stat(full)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	name := filepath.Base(full)
	if p == "" {
		name = "files"
	}
	if info.IsDir() {
		if r.URL.Query().Get("zip") != "1" {
			http.Error(w, "a folder downloads as a zip (zip=1)", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name + ".zip"}))
		if r.Method == http.MethodHead {
			return
		}
		if err := zipTo(r, w, filepath.Dir(full), name); err != nil {
			m.log.Warn("folder download", "path", p, "err", err)
		}
		return
	}
	f, err := os.Open(full)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	defer f.Close()
	disp := "inline"
	if r.URL.Query().Get("download") == "1" {
		disp = "attachment"
	}
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disp, map[string]string{"filename": name}))
	if ct := mime.TypeByExtension(strings.ToLower(filepath.Ext(name))); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, name, info.ModTime(), f)
}

// zipTo streams dir/name (a folder) as a zip, paths starting at name.
func zipTo(r *http.Request, w io.Writer, dir, name string) error {
	zw := newStreamZip(w)
	err := filepath.WalkDir(filepath.Join(dir, name), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if r.Context().Err() != nil {
			return r.Context().Err()
		}
		if strings.HasPrefix(d.Name(), ".maitre") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rp, _ := filepath.Rel(dir, p)
		return zw.add(p, filepath.ToSlash(rp), d)
	})
	if cerr := zw.close(); err == nil {
		err = cerr
	}
	return err
}

func (m *Manager) servePut(w http.ResponseWriter, r *http.Request, base, p string, mkdirs bool) {
	if p == "" {
		http.Error(w, "name the file", http.StatusBadRequest)
		return
	}
	for _, part := range strings.Split(p, "/") {
		if strings.HasPrefix(part, ".") {
			http.Error(w, fmt.Sprintf("%q: names cannot start with a dot", part), http.StatusBadRequest)
			return
		}
	}
	full := filepath.Join(base, p)
	dir := filepath.Dir(full)
	if mkdirs {
		if err := m.mkdirs(base, filepath.Dir(p)); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	} else if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		http.Error(w, "the folder it goes in does not exist", http.StatusNotFound)
		return
	}
	if _, err := os.Lstat(full); err == nil {
		http.Error(w, p+" "+ErrExists.Error(), http.StatusConflict)
		return
	}
	tmp, err := os.CreateTemp(dir, ".maitre-upload-*")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, r.Body); err != nil {
		tmp.Close()
		http.Error(w, "upload interrupted: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := tmp.Close(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_ = os.Chmod(tmp.Name(), 0o664)
	if t, err := time.Parse(time.RFC3339, r.URL.Query().Get("modified")); err == nil {
		_ = os.Chtimes(tmp.Name(), t, t)
	}
	// Link refuses to replace, unlike Rename: a file that appeared while
	// this one uploaded wins.
	if err := os.Link(tmp.Name(), full); err != nil {
		if errors.Is(err, fs.ErrExist) {
			http.Error(w, p+" "+ErrExists.Error(), http.StatusConflict)
			return
		}
		// Filesystems without hard links (mergerfs may refuse): rename.
		if err := os.Rename(tmp.Name(), full); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	m.own(full)
	w.WriteHeader(http.StatusCreated)
}

// mkdirs makes rel's missing folders under base, each given its owner.
func (m *Manager) mkdirs(base, r string) error {
	if r == "." || r == "" {
		return nil
	}
	cur := base
	for _, part := range strings.Split(r, "/") {
		cur = filepath.Join(cur, part)
		if err := os.Mkdir(cur, 0o775); err == nil {
			m.own(cur)
		} else if !errors.Is(err, fs.ErrExist) {
			return err
		}
	}
	return nil
}
