//go:build !linux

package owner

// As runs fn; acting as another user is Linux only.
func As(id uint32, fn func() error) error { return fn() }
