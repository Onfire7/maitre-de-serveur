//go:build !linux

package main

import "errors"

// Nodes run only on Linux; the binary is built elsewhere as the Desktop's
// netboot helper (decision 92).
func sethostname(string) error { return errors.New("not supported on this platform") }
