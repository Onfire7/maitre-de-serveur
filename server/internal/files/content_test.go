package files

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
)

func TestContentGetPut(t *testing.T) {
	m, _, dir := setup(t)
	allowed := true
	srv := httptest.NewServer(m.Handler(func(*http.Request) bool { return allowed }))
	defer srv.Close()
	url := srv.URL + ContentPath

	// A file, then a range of it.
	res, _ := http.Get(url + "?drive=d1&path=notes.txt")
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 || string(b) != "hi" || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/plain") {
		t.Fatalf("get: %d %q %s", res.StatusCode, b, res.Header.Get("Content-Type"))
	}
	req, _ := http.NewRequest("GET", url+"?drive=d1&path=Movies/Alien/alien.mkv", nil)
	req.Header.Set("Range", "bytes=10-19")
	res, _ = http.DefaultClient.Do(req)
	b, _ = io.ReadAll(res.Body)
	if res.StatusCode != http.StatusPartialContent || len(b) != 10 {
		t.Fatalf("range: %d %d", res.StatusCode, len(b))
	}
	// A folder only as a zip.
	if res, _ := http.Get(url + "?drive=d1&path=Movies"); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("folder without zip: %d", res.StatusCode)
	}
	res, _ = http.Get(url + "?drive=d1&path=Movies&zip=1")
	b, _ = io.ReadAll(res.Body)
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	if strings.Join(names, ",") != "Movies/,Movies/Alien/,Movies/Alien/alien.mkv" {
		t.Fatalf("zip entries %v", names)
	}
	// Escapes and missing files.
	if res, _ := http.Get(url + "?drive=d1&path=../../etc/passwd"); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("escape: %d", res.StatusCode)
	}

	put := func(q, body string) int {
		req, _ := http.NewRequest("PUT", url+q, strings.NewReader(body))
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return res.StatusCode
	}
	if c := put("?space=media&path=new.txt", "fresh"); c != http.StatusCreated {
		t.Fatalf("put: %d", c)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "pools/media/new.txt")); string(b) != "fresh" {
		t.Fatalf("written %q", b)
	}
	if c := put("?space=media&path=new.txt", "again"); c != http.StatusConflict {
		t.Fatalf("replace: %d", c)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "pools/media/new.txt")); string(b) != "fresh" {
		t.Fatal("a PUT replaced a file")
	}
	if c := put("?space=media&path=Shows/S1/e1.mkv", "x"); c != http.StatusNotFound {
		t.Fatalf("missing parent: %d", c)
	}
	if c := put("?space=media&path=Shows/S1/e1.mkv&mkdirs=1", "x"); c != http.StatusCreated {
		t.Fatalf("mkdirs: %d", c)
	}
	if c := put("?space=media&path=.hidden", "x"); c != http.StatusBadRequest {
		t.Fatalf("hidden: %d", c)
	}
	if entries, _ := os.ReadDir(filepath.Join(dir, "pools/media")); len(entries) != 2 {
		t.Fatalf("leftovers in the Pool: %v", entries)
	}
	allowed = false
	if res, _ := http.Get(url + "?drive=d1&path=notes.txt"); res.StatusCode != http.StatusForbidden {
		t.Fatalf("not allowed: %d", res.StatusCode)
	}
}

func waitJob(t *testing.T, m *Manager, id string) *maitrev1.Job {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		for _, j := range m.Jobs() {
			if j.GetJobId() == id && (j.GetState() == "done" || j.GetState() == "failed") {
				return j
			}
		}
	}
	t.Fatal("job did not finish")
	return nil
}

func TestZipAndUnzip(t *testing.T) {
	m, ctx, dir := setup(t)
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go m.Run(runCtx)
	d1 := &maitrev1.Scope{DriveId: "d1"}

	j, name, err := m.Zip(ctx, &maitrev1.ZipRequest{Scope: d1, Names: []string{"Movies", "notes.txt"}})
	if err != nil || name != "Archive.zip" || j.GetKind() != "zip" {
		t.Fatalf("zip: %v %q %v", err, name, j)
	}
	if j := waitJob(t, m, j.GetJobId()); j.GetState() != "done" || j.GetBytesDone() != 5002 {
		t.Fatalf("zip job %v", j)
	}
	// A second zip of the same picks the next free name.
	if _, name, _ := m.Zip(ctx, &maitrev1.ZipRequest{Scope: d1, Names: []string{"notes.txt"}, Name: "Archive"}); name != "Archive 2.zip" {
		t.Fatalf("second name %q", name)
	}

	j, folder, err := m.Unzip(ctx, &maitrev1.UnzipRequest{Scope: d1, Path: "Archive.zip"})
	if err != nil || folder != "Archive" {
		t.Fatalf("unzip: %v %q", err, folder)
	}
	if j := waitJob(t, m, j.GetJobId()); j.GetState() != "done" {
		t.Fatalf("unzip job %v", j)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "drives/d1/Archive/Movies/Alien/alien.mkv")); len(b) != 5000 {
		t.Fatalf("unzipped %d bytes", len(b))
	}

	// A tar.gz whose entry climbs out is refused and leaves nothing.
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	tw.WriteHeader(&tar.Header{Name: "../../evil.txt", Mode: 0o644, Size: 4, Typeflag: tar.TypeReg})
	tw.Write([]byte("evil"))
	tw.Close()
	os.WriteFile(filepath.Join(dir, "drives/d1/bad.tar"), buf.Bytes(), 0o644)
	j, _, _ = m.Unzip(ctx, &maitrev1.UnzipRequest{Scope: d1, Path: "bad.tar"})
	if j := waitJob(t, m, j.GetJobId()); j.GetState() != "failed" {
		t.Fatalf("zip-slip job %v", j)
	}
	if _, err := os.Stat(filepath.Join(dir, "evil.txt")); err == nil {
		t.Fatal("an entry escaped")
	}
	if _, err := os.Stat(filepath.Join(dir, "drives/d1/bad")); err == nil {
		t.Fatal("a failed unzip left its folder")
	}
	if _, _, err := m.Unzip(ctx, &maitrev1.UnzipRequest{Scope: d1, Path: "notes.txt"}); err == nil {
		t.Fatal("unzipped a text file")
	}
}
