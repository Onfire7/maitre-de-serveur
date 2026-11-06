package metrics

import "testing"

// A booted bootc Node answers statfs("/") with the deployment's composefs
// image — on Boulder, 8 MiB at 100% used — while the partition that holds
// the OS, the journal and the staged update is /sysroot. The gauge built
// to show a root filling up therefore showed a permanent false alarm. This
// is that case and the ones around it.
func TestSystemDiskDirPicksTheFilesystemWithTheSpace(t *testing.T) {
	const gib = 1 << 30
	for _, tc := range []struct {
		name          string
		sysroot, root [2]uint64 // total, used
		want          string
	}{
		{
			name:    "a booted bootc Node: / is the composefs image",
			sysroot: [2]uint64{32 * gib, 23 * gib}, root: [2]uint64{8 << 20, 8 << 20},
			want: "/sysroot",
		},
		{
			name:    "a plain Linux box: there is no /sysroot",
			sysroot: [2]uint64{0, 0}, root: [2]uint64{237 * gib, 180 * gib},
			want: "/",
		},
		{
			name:    "an installer or initramfs: /sysroot is the target",
			sysroot: [2]uint64{32 * gib, 2 * gib}, root: [2]uint64{0, 0},
			want: "/sysroot",
		},
		{
			name:    "neither answers: / , so the reading is zero rather than wrong",
			sysroot: [2]uint64{0, 0}, root: [2]uint64{0, 0},
			want: "/",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stat := func(dir string) (uint64, uint64) {
				switch dir {
				case "/sysroot":
					return tc.sysroot[0], tc.sysroot[1]
				case "/":
					return tc.root[0], tc.root[1]
				}
				t.Fatalf("statfs of an unexpected path %q", dir)
				return 0, 0
			}
			if got := systemDiskDir(stat); got != tc.want {
				t.Errorf("systemDiskDir = %q, want %q", got, tc.want)
			}
		})
	}
}
