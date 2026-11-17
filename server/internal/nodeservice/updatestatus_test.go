package nodeservice

import (
	"context"
	"testing"
	"time"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/update"
)

// bootc as GetUpdateStatus asks it: a booted image and nothing else.
type bootc struct{}

func (bootc) Run(context.Context, string, ...string) (string, error) {
	return `{"status":{"booted":{"image":{"image":{"image":"ghcr.io/onfire7/maitre-server:main"},"imageDigest":"sha256:a"}}}}`, nil
}
func (bootc) Switch(context.Context, string, func(update.Progress)) error { return nil }
func (bootc) Upgrade(context.Context, func(update.Progress)) error        { return nil }

// updatedRun took total minutes from start to back up, down of them with
// its Services down.
func updatedRun(total, down int64) *maitrev1.UpdateRun {
	end := int64(1_700_000_000_000)
	return &maitrev1.UpdateRun{
		StartedUnixMs: end - total*60_000, RestartedUnixMs: end - down*60_000, EndedUnixMs: end,
		Outcome: maitrev1.UpdateRun_OUTCOME_UPDATED,
	}
}

// GetUpdateStatus says how long this Node's updates usually take, from
// its own runs, so the Console only shows it; no figure with too little
// history.
func TestGetUpdateStatusSaysHowLongUpdatesTake(t *testing.T) {
	for _, c := range []struct {
		name        string
		runs        []*maitrev1.UpdateRun
		total, down time.Duration
	}{
		{"enough history", []*maitrev1.UpdateRun{updatedRun(6, 2), updatedRun(7, 2), updatedRun(5, 3)}, 6 * time.Minute, 2 * time.Minute},
		{"too little", []*maitrev1.UpdateRun{updatedRun(6, 2), updatedRun(7, 2)}, 0, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := &Service{Updates: update.New(bootc{}, nil), Runs: func() []*maitrev1.UpdateRun { return c.runs }}
			resp, err := s.GetUpdateStatus(context.Background(), &maitrev1.GetUpdateStatusRequest{})
			if err != nil {
				t.Fatal(err)
			}
			st := resp.GetStatus()
			if got, want := st.GetTypicalUpdateMs(), c.total.Milliseconds(); got != want {
				t.Errorf("typical update %d ms, want %d", got, want)
			}
			if got, want := st.GetTypicalDowntimeMs(), c.down.Milliseconds(); got != want {
				t.Errorf("typical downtime %d ms, want %d", got, want)
			}
		})
	}
}
