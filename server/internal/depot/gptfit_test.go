package depot

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A stick image laid on a bigger disk gets its backup GPT at the disk's end
// and a protective MBR across it; fdisk then has nothing to say.
func TestFitGPTMovesTheBackupToTheDisksEnd(t *testing.T) {
	l := Layout{Dir: t.TempDir()}
	if err := os.MkdirAll(l.Boot(), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range BootFiles {
		if err := os.WriteFile(filepath.Join(l.Boot(), f), []byte("contents of "+f), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	img := filepath.Join(t.TempDir(), "stick.img")
	if err := BuildStick(l, img, StickParams{Image: "x/maitre-server:dev"}, nil); err != nil {
		t.Fatal(err)
	}
	src, _ := os.Open(img)
	defer src.Close()
	st, _ := src.Stat()
	big := filepath.Join(t.TempDir(), "disk")
	dst, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	if _, err := io.Copy(dst, src); err != nil {
		t.Fatal(err)
	}
	size := st.Size() + 256<<20
	if err := dst.Truncate(size); err != nil {
		t.Fatal(err)
	}
	moved, err := FitGPT(dst, size/sector)
	if err != nil || !moved {
		t.Fatalf("FitGPT: moved=%v err=%v", moved, err)
	}
	le := binary.LittleEndian
	last := uint64(size/sector - 1)
	hdr := func(lba uint64) []byte {
		b := make([]byte, sector)
		dst.ReadAt(b, int64(lba)*sector)
		if !bytes.Equal(b[:8], gptSignature) {
			t.Fatalf("no GPT header at LBA %d", lba)
		}
		n := le.Uint32(b[12:16])
		c := append([]byte(nil), b[:n]...)
		le.PutUint32(c[16:20], 0)
		if crc32.ChecksumIEEE(c) != le.Uint32(b[16:20]) {
			t.Fatalf("bad header CRC at LBA %d", lba)
		}
		return b
	}
	p, b := hdr(1), hdr(last)
	if le.Uint64(p[32:40]) != last || le.Uint64(b[32:40]) != 1 || le.Uint64(b[24:32]) != last {
		t.Error("headers do not point at each other")
	}
	mbr := make([]byte, sector)
	dst.ReadAt(mbr, 0)
	if got := le.Uint32(mbr[446+12:]); uint64(got) != last {
		t.Errorf("protective MBR covers %d sectors, want %d", got, last)
	}
	if again, err := FitGPT(dst, size/sector); err != nil || again {
		t.Errorf("a second FitGPT changed something: %v %v", again, err)
	}
	if fdisk, err := exec.LookPath("fdisk"); err == nil {
		out, _ := exec.Command(fdisk, "-l", big).CombinedOutput()
		for _, bad := range []string{"mismatch", "not on the end", "corrupt", "invalid"} {
			if strings.Contains(strings.ToLower(string(out)), bad) {
				t.Errorf("fdisk: %s", out)
			}
		}
	}
	if fsck, err := exec.LookPath("fsck.fat"); err == nil {
		part := filepath.Join(t.TempDir(), "part")
		pf, _ := os.Create(part)
		io.Copy(pf, io.NewSectionReader(dst, 1<<20, st.Size()-2<<20))
		pf.Close()
		out, _ := exec.Command(fsck, "-n", part).CombinedOutput()
		if strings.Contains(string(out), "Fixing") || strings.Contains(string(out), "Invalid") {
			t.Errorf("fsck.fat: %s", out)
		}
	}
}

func TestFitGPTLeavesOtherDisksAlone(t *testing.T) {
	f, _ := os.Create(filepath.Join(t.TempDir(), "plain"))
	defer f.Close()
	f.Truncate(4 << 20)
	if moved, err := FitGPT(f, (4<<20)/sector); moved || err != nil {
		t.Errorf("touched a disk without a GPT: %v %v", moved, err)
	}
}
