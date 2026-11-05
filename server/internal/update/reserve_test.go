package update

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

// Decision 324: room for an update is held before it is needed. bootc
// stages into ostree's repository under /sysroot, and root is sysroot, so
// the reserve has to be a file inside root rather than a partition.
func TestTheReserveIsHeldAndHandedBack(t *testing.T) {
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	old := ReservePath
	defer func() { ReservePath = old }()
	ReservePath = filepath.Join(t.TempDir(), "reserve")

	// 110% of the image, and the constant when it cannot be measured.
	image := int64(4) << 30
	EnsureReserve(quiet, image)
	st, err := os.Stat(ReservePath)
	if err != nil {
		t.Fatal(err)
	}
	if want := int64(float64(image) * ReserveFactor); st.Size() != want {
		t.Fatalf("reserved %d, want %d", st.Size(), want)
	}
	// Idempotent: a reserve already big enough is left exactly as it is.
	before := st.ModTime()
	EnsureReserve(quiet, image)
	if st, err = os.Stat(ReservePath); err != nil || !st.ModTime().Equal(before) {
		t.Fatalf("the reserve was rewritten: %v %v", st, err)
	}
	// An update hands it back, which is what it was for, and twice is not
	// an error: a Node that never had one still stages.
	DropReserve(quiet)
	if _, err := os.Stat(ReservePath); !os.IsNotExist(err) {
		t.Fatalf("the reserve survived the update: %v", err)
	}
	DropReserve(quiet)

	EnsureReserve(quiet, 0)
	if st, err = os.Stat(ReservePath); err != nil || st.Size() != MinReserve {
		t.Fatalf("unmeasured image: %v %v", st, err)
	}
}
