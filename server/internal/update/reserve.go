package update

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
)

// The staging reserve (decision 324): room for an update, held before it is
// needed rather than hoped for when it is.
//
// bootc stages into ostree's repository under /sysroot, and root *is*
// sysroot, so the space an update needs has to be inside the root
// partition — a separate partition cannot receive staging at all. xfs has
// no tunable reserve of ext4's kind, so the reserve is a file: allocated
// once, deleted the moment an update starts, and made again afterwards.
//
// 110% of the image is generous rather than tight, which is the point:
// only a large version jump approaches it, and an image that outgrows its
// reserve is what the rescue boot of decision 325 is for.
// ReservePath is on root deliberately: /var is the writable part of the
// root filesystem, which is the filesystem being reserved. A variable so a
// test need not write to /var.
var ReservePath = "/var/lib/maitre-update-reserve"

const (
	// ReserveFactor over the image's installed size.
	ReserveFactor = 1.1
	// MinReserve stands in when the image cannot be measured: about 2 GB
	// installed plus the tenth, as 2252 MiB.
	MinReserve = int64(2252) << 20
)

// EnsureReserve holds room for the next update, idempotently. imageBytes
// is the booted image's installed size; 0 uses MinReserve.
//
// Best effort by design: a Node whose root is already too full to hold the
// reserve is a Node that needs the reserve most and can no longer have it,
// and refusing to start over that would help nobody. It says so instead,
// and the greenboot space check says it again on every boot.
func EnsureReserve(log *slog.Logger, imageBytes int64) {
	want := MinReserve
	if imageBytes > 0 {
		if n := int64(float64(imageBytes) * ReserveFactor); n > want {
			want = n
		}
	}
	if st, err := os.Stat(ReservePath); err == nil && st.Size() >= want {
		return
	}
	if err := os.MkdirAll(filepath.Dir(ReservePath), 0o755); err != nil {
		log.Warn("update reserve", "err", err)
		return
	}
	// fallocate, not a written file: the blocks are reserved without the
	// write, which is all a reserve is for.
	if out, err := exec.Command("fallocate", "-l", fmt.Sprint(want), ReservePath).CombinedOutput(); err != nil {
		_ = os.Remove(ReservePath) // a partial reserve is worse than none
		log.Warn("could not reserve room for the next update; an update may not fit",
			"bytes", want, "path", ReservePath, "err", err, "out", string(out))
		return
	}
	log.Info("room reserved for the next update (decision 324)", "bytes", want, "path", ReservePath)
}

// DropReserve gives the reserve back, which is what it was for. Called
// before staging: the space it held is the space bootc is about to want.
func DropReserve(log *slog.Logger) {
	if err := os.Remove(ReservePath); err == nil {
		log.Info("released the reserved room for this update (decision 324)", "path", ReservePath)
	} else if !os.IsNotExist(err) {
		log.Warn("could not release the update reserve", "path", ReservePath, "err", err)
	}
}
