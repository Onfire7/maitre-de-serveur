//go:build !linux

package pool

// IsFallback is Linux-only; the Desktop helper builds never assemble a Pool.
func IsFallback() bool { return false }
