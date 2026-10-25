// Package backup copies a Node's config partition (its key and the Fleet
// database) onto every Drive of the Node (decision 102), and lets the
// Installer find and restore such copies. The copy is not encrypted: disk
// encryption is not on the roadmap, and the OS disk holds the same secrets.
package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/store"
)

// Dir is where a backup lives on a Drive, under the Drive's root.
const Dir = ".maitre/backup"

// Meta describes a backup for the Installer and the Console.
type Meta struct {
	NodeID      string `json:"node_id"`
	NodeName    string `json:"node_name"`
	FleetID     string `json:"fleet_id"`
	FleetName   string `json:"fleet_name"`
	SavedUnixMs int64  `json:"saved_unix_ms"`
}

// Files of the config partition that make a Node.
var files = []string{"node/identity.key", "node/identity.pub"}

// Write copies stateDir (the config partition) into driveRoot/.maitre/backup/<node_id>/,
// replacing any previous copy atomically. st supplies a consistent database copy.
func Write(ctx context.Context, st *store.Store, stateDir, driveRoot string, meta Meta) error {
	final := filepath.Join(driveRoot, Dir, meta.NodeID)
	tmp := final + ".tmp"
	_ = os.RemoveAll(tmp)
	if err := os.MkdirAll(filepath.Join(tmp, "node"), 0o700); err != nil {
		return err
	}
	for _, f := range files {
		if err := copyFile(filepath.Join(stateDir, f), filepath.Join(tmp, f)); err != nil {
			return err
		}
	}
	if err := st.Snapshot(ctx, filepath.Join(tmp, "fleet.db")); err != nil {
		return fmt.Errorf("database snapshot: %w", err)
	}
	meta.SavedUnixMs = time.Now().UnixMilli()
	mb, _ := json.MarshalIndent(meta, "", "  ")
	if err := os.WriteFile(filepath.Join(tmp, "meta.json"), mb, 0o600); err != nil {
		return err
	}
	_ = os.RemoveAll(final)
	return os.Rename(tmp, final)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// Found is a backup located on a mounted filesystem.
type Found struct {
	Meta   Meta
	Dir    string // the backup directory while its filesystem is mounted
	Device string
	Disk   string
}

// ScanDir finds backups under one mounted root.
func ScanDir(root, device, disk string) []Found {
	entries, err := os.ReadDir(filepath.Join(root, Dir))
	if err != nil {
		return nil
	}
	var out []Found
	for _, e := range entries {
		if !e.IsDir() || strings.HasSuffix(e.Name(), ".tmp") {
			continue
		}
		dir := filepath.Join(root, Dir, e.Name())
		mb, err := os.ReadFile(filepath.Join(dir, "meta.json"))
		if err != nil {
			continue
		}
		var m Meta
		if json.Unmarshal(mb, &m) != nil || m.NodeID == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, "fleet.db")); err != nil {
			continue
		}
		out = append(out, Found{Meta: m, Dir: dir, Device: device, Disk: disk})
	}
	return out
}

// Partition is a candidate filesystem the Installer may look at.
type Partition struct {
	Device string
	Disk   string
	FSType string
}

// Scan mounts each partition read-only in turn and lists the backups it
// holds. Partitions that will not mount are skipped.
func Scan(ctx context.Context, parts []Partition) []*maitrev1.Backup {
	var out []*maitrev1.Backup
	for _, p := range parts {
		switch p.FSType {
		case "ext4", "ext3", "ext2", "xfs", "btrfs":
		default:
			continue
		}
		mnt, err := os.MkdirTemp("", "maitre-scan-")
		if err != nil {
			continue
		}
		if err := exec.CommandContext(ctx, "mount", "-o", "ro,noatime", p.Device, mnt).Run(); err != nil {
			os.Remove(mnt)
			continue
		}
		for _, f := range ScanDir(mnt, p.Device, p.Disk) {
			out = append(out, &maitrev1.Backup{NodeId: f.Meta.NodeID, NodeName: f.Meta.NodeName, FleetId: f.Meta.FleetID, FleetName: f.Meta.FleetName, SavedUnixMs: f.Meta.SavedUnixMs, Device: p.Device, Disk: p.Disk})
		}
		_ = exec.CommandContext(ctx, "umount", mnt).Run()
		os.Remove(mnt)
	}
	return out
}

// Restore copies the backup at dir into stateDir (the new config partition).
func Restore(dir, stateDir string) error {
	if err := os.MkdirAll(filepath.Join(stateDir, "node"), 0o700); err != nil {
		return err
	}
	for _, f := range append(append([]string{}, files...), "fleet.db") {
		if err := copyFile(filepath.Join(dir, f), filepath.Join(stateDir, f)); err != nil {
			return fmt.Errorf("restore %s: %w", f, err)
		}
	}
	return nil
}

// RestoreFromDevice mounts device read-only, restores the backup for nodeID
// into stateDir, and unmounts.
func RestoreFromDevice(ctx context.Context, device, nodeID, stateDir string) error {
	mnt, err := os.MkdirTemp("", "maitre-restore-")
	if err != nil {
		return err
	}
	defer os.Remove(mnt)
	if out, err := exec.CommandContext(ctx, "mount", "-o", "ro", device, mnt).CombinedOutput(); err != nil {
		return fmt.Errorf("mount %s: %w: %s", device, err, strings.TrimSpace(string(out)))
	}
	defer exec.CommandContext(context.Background(), "umount", mnt).Run()
	for _, f := range ScanDir(mnt, device, "") {
		if f.Meta.NodeID == nodeID {
			return Restore(f.Dir, stateDir)
		}
	}
	return errors.New("no backup for that Node on " + device)
}
