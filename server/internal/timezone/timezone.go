// Package timezone keeps a Node's clock in the Fleet's time zone (decision
// 282). The Fleet's zone is the kind "fleet_settings" Record with id
// "fleet", signed by a Trusted Device; a Node that reads a zone there sets
// its own to match, and Services whose TZ setting is "auto" run in it.
package timezone

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/store"
)

// Kind and ID name the Fleet's settings record.
const (
	Kind = "fleet_settings"
	ID   = "fleet"
)

// Auto is the TZ setting value that follows the Fleet's zone.
const Auto = "auto"

// Runner runs a command, as services.Runner does.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) (string, error)
}

// Fleet is the Fleet's time zone, or "" when none is set or the name is
// not a zone this Node knows.
func Fleet(ctx context.Context, st *store.Store) string {
	if st == nil {
		return ""
	}
	var s maitrev1.FleetSettings
	if _, err := st.Get(ctx, Kind, ID, &s); err != nil {
		return ""
	}
	if !Valid(s.GetTimeZone()) {
		return ""
	}
	return s.GetTimeZone()
}

// Local is the zone this Node's clock is set to, read from /etc/localtime;
// "UTC" when that is not a link into zoneinfo.
func Local() string {
	if link, err := os.Readlink("/etc/localtime"); err == nil {
		if i := strings.Index(link, "zoneinfo/"); i >= 0 {
			return link[i+len("zoneinfo/"):]
		}
	}
	return "UTC"
}

// Valid reports whether name is an IANA zone this machine has: a plain
// Area/City (or UTC) that time.LoadLocation finds in the system's zoneinfo.
func Valid(name string) bool {
	if name == "" || name == "Local" || strings.HasPrefix(name, "/") || strings.Contains(name, "..") || strings.ContainsAny(name, " \t\n\r") {
		return false
	}
	_, err := time.LoadLocation(name)
	return err == nil
}

// Resolve is the zone a Service with TZ=auto runs in: the Fleet's, else
// this Node's own.
func Resolve(ctx context.Context, st *store.Store) string {
	if tz := Fleet(ctx, st); tz != "" {
		return tz
	}
	return Local()
}

// Apply sets the Node's clock to the Fleet's zone when it differs. It
// reports whether the zone changed; a Fleet with no zone set, or a zone
// this Node does not know, changes nothing. The change goes through
// timedatectl, which rewrites /etc/localtime and tells running systemd
// units that ask.
func Apply(ctx context.Context, st *store.Store, run Runner, log *slog.Logger) (changed bool, err error) {
	var s maitrev1.FleetSettings
	if st == nil {
		return false, nil
	}
	if _, err := st.Get(ctx, Kind, ID, &s); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	want := s.GetTimeZone()
	if want == "" {
		return false, nil
	}
	if !Valid(want) {
		return false, fmt.Errorf("the Fleet's time zone %q is not one this Node knows", want)
	}
	if Local() == want {
		return false, nil
	}
	if _, err := run.Run(ctx, "timedatectl", "set-timezone", want); err != nil {
		return false, fmt.Errorf("timedatectl set-timezone %s: %w", want, err)
	}
	if log != nil {
		log.Info("time zone set to the Fleet's", "zone", want)
	}
	return true, nil
}
