package files

import (
	"archive/tar"
	"archive/zip"
	"compress/bzip2"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
)

// Zip queues a job that puts items of one folder into a zip beside them
// (decision 184) and returns it with the archive's name.
func (m *Manager) Zip(ctx context.Context, req *maitrev1.ZipRequest) (*maitrev1.Job, string, error) {
	base, err := m.base(ctx, req.GetScope())
	if err != nil {
		return nil, "", err
	}
	folder, err := rel(req.GetFolder())
	if err != nil {
		return nil, "", err
	}
	dir := filepath.Join(base, folder)
	if len(req.GetNames()) == 0 {
		return nil, "", errors.New("pick something to zip")
	}
	var total uint64
	for _, n := range req.GetNames() {
		if n == "" || strings.ContainsRune(n, '/') || n == "." || n == ".." {
			return nil, "", fmt.Errorf("%q is not a name in the folder", n)
		}
		info, err := os.Lstat(filepath.Join(dir, n))
		if err != nil {
			return nil, "", fmt.Errorf("%s: not found", n)
		}
		if info.IsDir() {
			b, _ := walk(filepath.Join(dir, n))
			total += b
		} else {
			total += uint64(info.Size())
		}
	}
	name := strings.TrimSpace(req.GetName())
	if name == "" {
		name = "Archive.zip"
		if len(req.GetNames()) == 1 {
			name = req.GetNames()[0] + ".zip"
		}
	}
	if strings.ContainsRune(name, '/') || strings.HasPrefix(name, ".") {
		return nil, "", fmt.Errorf("%q cannot be an archive name", name)
	}
	if !strings.HasSuffix(strings.ToLower(name), ".zip") {
		name += ".zip"
	}
	name = freeName(dir, strings.TrimSuffix(name, filepath.Ext(name)), ".zip")
	what := fmt.Sprintf("%d items", len(req.GetNames()))
	if len(req.GetNames()) == 1 {
		what = req.GetNames()[0]
	}
	names := append([]string(nil), req.GetNames()...)
	j := &job{pb: &maitrev1.Job{State: "queued", Kind: "zip", Summary: fmt.Sprintf("Zip %s into %s", what, name), BytesTotal: total, Current: filepath.Join(folder, name)}, owner: m.ownerOf(req.GetScope().GetSpaceId())}
	j.run = func(ctx context.Context, j *job) error { return m.zip(ctx, j, dir, names, name) }
	return m.enqueue(j), name, nil
}

func (m *Manager) zip(ctx context.Context, j *job, dir string, names []string, name string) error {
	tmp, err := os.CreateTemp(dir, ".maitre-zip-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	zw := zip.NewWriter(tmp)
	var done uint64
	for _, n := range names {
		err := filepath.WalkDir(filepath.Join(dir, n), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if strings.HasPrefix(d.Name(), ".maitre") {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			r, _ := filepath.Rel(dir, p)
			if d.IsDir() {
				h, _ := zip.FileInfoHeader(info)
				h.Name = filepath.ToSlash(r) + "/"
				_, err := zw.CreateHeader(h)
				return err
			}
			if !info.Mode().IsRegular() {
				return nil // links and devices stay out
			}
			h, err := zip.FileInfoHeader(info)
			if err != nil {
				return err
			}
			h.Name = filepath.ToSlash(r)
			h.Method = zip.Deflate
			if compressed[strings.ToLower(filepath.Ext(p))] {
				h.Method = zip.Store
			}
			w, err := zw.CreateHeader(h)
			if err != nil {
				return err
			}
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			defer f.Close()
			j.mu.Lock()
			j.pb.Current = r
			j.mu.Unlock()
			_, err = io.Copy(w, &progressReader{ctx: ctx, r: f, add: func(n int) {
				done += uint64(n)
				j.mu.Lock()
				j.pb.BytesDone = done
				j.mu.Unlock()
			}})
			return err
		})
		if err != nil {
			zw.Close()
			tmp.Close()
			return err
		}
	}
	if err := zw.Close(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	final := filepath.Join(dir, name)
	if _, err := os.Lstat(final); err == nil {
		final = filepath.Join(dir, freeName(dir, strings.TrimSuffix(name, ".zip"), ".zip"))
	}
	if err := os.Chmod(tmp.Name(), 0o664); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), final); err != nil {
		return err
	}
	m.own(final)
	return nil
}

// Unzip queues a job that unpacks an archive into a folder beside it.
func (m *Manager) Unzip(ctx context.Context, req *maitrev1.UnzipRequest) (*maitrev1.Job, string, error) {
	base, err := m.base(ctx, req.GetScope())
	if err != nil {
		return nil, "", err
	}
	r, err := rel(req.GetPath())
	if err != nil || r == "" {
		return nil, "", errors.New("pick an archive")
	}
	src := filepath.Join(base, r)
	info, err := os.Stat(src)
	if err != nil || !info.Mode().IsRegular() {
		return nil, "", fmt.Errorf("%s: not a file", r)
	}
	stem, kind := archiveKind(filepath.Base(src))
	if kind == "" {
		return nil, "", fmt.Errorf("%s: only zip, tar, tar.gz and tar.bz2 unpack here", filepath.Base(src))
	}
	dir := filepath.Dir(src)
	folder := freeName(dir, stem, "")
	j := &job{pb: &maitrev1.Job{State: "queued", Kind: "unzip", Summary: fmt.Sprintf("Unzip %s into %s", filepath.Base(src), folder), BytesTotal: uint64(info.Size()), Current: r}, owner: m.ownerOf(req.GetScope().GetSpaceId())}
	j.run = func(ctx context.Context, j *job) error { return m.unzip(ctx, j, src, kind, dir, folder) }
	return m.enqueue(j), folder, nil
}

// archiveKind splits an archive's name into the folder name it unpacks to
// and its kind: zip, tar, tgz, tbz; "" when it is not one.
func archiveKind(name string) (stem, kind string) {
	l := strings.ToLower(name)
	for _, k := range []struct{ ext, kind string }{{".tar.gz", "tgz"}, {".tgz", "tgz"}, {".tar.bz2", "tbz"}, {".tbz2", "tbz"}, {".tar", "tar"}, {".zip", "zip"}} {
		if strings.HasSuffix(l, k.ext) && len(name) > len(k.ext) {
			return name[:len(name)-len(k.ext)], k.kind
		}
	}
	return "", ""
}

func (m *Manager) unzip(ctx context.Context, j *job, src, kind, dir, folder string) error {
	tmp, err := os.MkdirTemp(dir, ".maitre-unzip-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	var done uint64
	progress := func(n int) {
		done += uint64(n)
		j.mu.Lock()
		j.pb.BytesDone = done
		j.mu.Unlock()
	}
	if kind == "zip" {
		info, _ := f.Stat()
		zr, err := zip.NewReader(f, info.Size())
		if err != nil {
			return fmt.Errorf("not a readable zip: %w", err)
		}
		var total uint64
		for _, e := range zr.File {
			total += e.UncompressedSize64
		}
		j.mu.Lock()
		j.pb.BytesTotal = total
		j.mu.Unlock()
		for _, e := range zr.File {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			dst, err := inside(tmp, e.Name)
			if err != nil {
				return err
			}
			if e.FileInfo().IsDir() {
				if err := os.MkdirAll(dst, 0o775); err != nil {
					return err
				}
				continue
			}
			if !e.Mode().IsRegular() {
				continue
			}
			rc, err := e.Open()
			if err != nil {
				return err
			}
			err = writeFile(ctx, dst, rc, e.Mode(), progress)
			rc.Close()
			if err != nil {
				return err
			}
		}
	} else {
		// Progress counts the archive's own bytes as they are read.
		var r io.Reader = &progressReader{ctx: ctx, r: f, add: progress}
		switch kind {
		case "tgz":
			gz, err := gzip.NewReader(r)
			if err != nil {
				return fmt.Errorf("not a readable tar.gz: %w", err)
			}
			defer gz.Close()
			r = gz
		case "tbz":
			r = bzip2.NewReader(r)
		}
		tr := tar.NewReader(r)
		for {
			h, err := tr.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return fmt.Errorf("not a readable tar: %w", err)
			}
			dst, err := inside(tmp, h.Name)
			if err != nil {
				return err
			}
			switch h.Typeflag {
			case tar.TypeDir:
				if err := os.MkdirAll(dst, 0o775); err != nil {
					return err
				}
			case tar.TypeReg:
				if err := writeFile(ctx, dst, tr, h.FileInfo().Mode(), nil); err != nil {
					return err
				}
			}
		}
	}
	final := filepath.Join(dir, folder)
	if _, err := os.Lstat(final); err == nil {
		final = filepath.Join(dir, freeName(dir, folder, ""))
	}
	if err := os.Chmod(tmp, 0o775); err != nil {
		return err
	}
	if err := os.Rename(tmp, final); err != nil {
		return err
	}
	m.ownTree(final)
	return nil
}

// inside joins an archive entry's name to dir, refusing names that would
// land outside it.
func inside(dir, name string) (string, error) {
	c := path.Clean(strings.ReplaceAll(name, "\\", "/"))
	if c == ".." || strings.HasPrefix(c, "../") || path.IsAbs(c) {
		return "", fmt.Errorf("%q would land outside the folder", name)
	}
	if c == "." {
		return dir, nil
	}
	p := filepath.Join(dir, filepath.FromSlash(c))
	if !strings.HasPrefix(p, dir+string(filepath.Separator)) {
		return "", fmt.Errorf("%q would land outside the folder", name)
	}
	return p, nil
}

func writeFile(ctx context.Context, dst string, r io.Reader, mode fs.FileMode, progress func(int)) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o775); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode.Perm()|0o664)
	if err != nil {
		return err
	}
	var src io.Reader = r
	if progress != nil {
		src = &progressReader{ctx: ctx, r: r, add: progress}
	} else {
		src = &progressReader{ctx: ctx, r: r, add: func(int) {}}
	}
	if _, err := io.Copy(out, src); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// freeName is stem+ext, or "stem 2"+ext, "stem 3"+ext… when taken in dir.
func freeName(dir, stem, ext string) string {
	name := stem + ext
	for i := 2; ; i++ {
		if _, err := os.Lstat(filepath.Join(dir, name)); errors.Is(err, fs.ErrNotExist) {
			return name
		}
		name = fmt.Sprintf("%s %d%s", stem, i, ext)
	}
}

type progressReader struct {
	ctx context.Context
	r   io.Reader
	add func(int)
}

func (p *progressReader) Read(b []byte) (int, error) {
	if err := p.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := p.r.Read(b)
	if n > 0 {
		p.add(n)
	}
	return n, err
}

// compressed are extensions whose contents do not shrink further; Zip
// stores them as they are.
var compressed = map[string]bool{}

func init() {
	for _, e := range strings.Fields(".jpg .jpeg .png .gif .webp .heic .heif .avif .mp4 .m4v .mkv .mov .avi .webm .wmv .mp3 .m4a .aac .flac .ogg .opus .zip .gz .tgz .bz2 .xz .zst .7z .rar .docx .xlsx .pptx .odt .ods .epub .cbz") {
		compressed[e] = true
	}
}

// streamZip writes a zip to a stream (a folder download), storing
// already-compressed files.
type streamZip struct{ zw *zip.Writer }

func newStreamZip(w io.Writer) *streamZip { return &streamZip{zw: zip.NewWriter(w)} }

func (s *streamZip) add(p, name string, d fs.DirEntry) error {
	info, err := d.Info()
	if err != nil {
		return err
	}
	h, err := zip.FileInfoHeader(info)
	if err != nil {
		return err
	}
	if d.IsDir() {
		h.Name = name + "/"
		_, err := s.zw.CreateHeader(h)
		return err
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	h.Name = name
	h.Method = zip.Deflate
	if compressed[strings.ToLower(filepath.Ext(p))] {
		h.Method = zip.Store
	}
	w, err := s.zw.CreateHeader(h)
	if err != nil {
		return err
	}
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(w, f)
	return err
}

func (s *streamZip) close() error { return s.zw.Close() }
