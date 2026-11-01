package filesservice

import (
	"errors"
	"fmt"
	"testing"

	"connectrpc.com/connect/v2"

	"github.com/Onfire7/maitre-de-serveur/server/internal/files"
)

// Decision 283: a listing error's code says whose fault it is.
func TestCodeFor(t *testing.T) {
	for err, want := range map[error]connect.Code{
		fmt.Errorf("photos: %w", files.ErrUnavailable): connect.CodeUnavailable,
		fmt.Errorf("photos: %w", files.ErrGone):        connect.CodeNotFound,
		errors.New("bad path"):                         connect.CodeInvalidArgument,
	} {
		if got := codeFor(err); got != want {
			t.Errorf("%v: got %v, want %v", err, got, want)
		}
	}
}
