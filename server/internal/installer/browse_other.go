//go:build !unix

package installer

// fsUsage is unknown off Unix; the Installer only runs on Linux.
func fsUsage(string) (used, total uint64) { return 0, 0 }
