//go:build windows

package metrics

// The agent never runs on Windows; the binary is built there only as the
// Desktop's netboot helper (decision 92).
func disk(string) (total, used uint64) { return 0, 0 }
