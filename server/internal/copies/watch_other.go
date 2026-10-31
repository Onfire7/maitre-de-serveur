//go:build !linux

package copies

import "context"

// Watch does nothing where there is no inotify; the hourly pass remains.
func (m *Manager) Watch(ctx context.Context) { <-ctx.Done() }
