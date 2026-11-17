package terminal

import "time"

// SetKeepFinished shortens how long an unseen finished Terminal is kept;
// the returned func puts it back.
func SetKeepFinished(d time.Duration) func() {
	old := keepFinished.Swap(int64(d))
	return func() { keepFinished.Store(old) }
}
