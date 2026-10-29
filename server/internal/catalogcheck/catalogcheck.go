// Package catalogcheck is the Catalog's second check (plan C8) for one
// entry: it renders the entry as a Node installs it, has Podman's Quadlet
// read the units, and holds the protected options it needs to what the
// entry declares and to what Maître supports
// (docs/popularity-and-grants.md, G2).
package catalogcheck

import (
	"context"
	"errors"
	"fmt"

	"github.com/Onfire7/maitre-de-serveur/server/internal/catalog"
	"github.com/Onfire7/maitre-de-serveur/server/internal/quadlet"
	"github.com/Onfire7/maitre-de-serveur/server/internal/services"
)

// SampleDevice fills a device setting, which has no value until a person
// picks one, so the check sees the option it adds.
const SampleDevice = "/dev/maitre-check-device"

// Result is an entry's check: the units it renders to with every feature
// on, and what is wrong with it.
type Result struct {
	Units    map[string]string
	Problems []string
	// NoQuadlet: Podman's Quadlet is not installed here, so only Maître's
	// own checks ran.
	NoQuadlet bool
}

// Check checks one entry.
func Check(ctx context.Context, e *catalog.Manifest) (*Result, error) {
	res := &Result{}
	for _, u := range e.Unfinished {
		res.Problems = append(res.Problems, "unfinished: "+u)
	}
	settings := map[string]string{}
	for _, s := range e.Settings {
		if s.Type == "device" {
			settings[s.Key] = SampleDevice
		}
	}
	on, err := render(ctx, e, settings, nil, res)
	if err != nil {
		return nil, err
	}
	res.Units = on.Units
	var off *services.CheckResult
	if fs := e.Features(); len(fs) > 0 {
		if off, err = render(ctx, e, settings, fs, res); err != nil {
			return nil, fmt.Errorf("with %v off: %w", fs, err)
		}
	}
	declared := func(p services.ProtectedOption) *catalog.Grant {
		for i, g := range e.Grants {
			if g.Matches(p.Component, p.Option) {
				return &e.Grants[i]
			}
		}
		return nil
	}
	for _, p := range on.Protected {
		if declared(p) != nil {
			continue
		}
		msg := fmt.Sprintf("needs %s but does not declare it; add it under grants: with why", where(p))
		if ok, needs := catalog.Support(p.Option); !ok {
			msg += fmt.Sprintf(", once Maître supports it: that needs %s", needs)
		}
		res.Problems = append(res.Problems, msg)
	}
	if off != nil {
		for _, p := range off.Protected {
			if g := declared(p); g != nil && g.Optional != "" {
				res.Problems = append(res.Problems, fmt.Sprintf("%s is optional (%s) but stays in the unit with %s off; put it inside {{ if .Grants.%s }}", where(p), g.Optional, g.Optional, g.Optional))
			}
		}
	}
	if len(e.Components) > 0 {
		for _, p := range on.Protected {
			if p.Option == "Network=host" {
				res.Problems = append(res.Problems, fmt.Sprintf("%s is not supported yet in an entry of several components: Maître needs to leave out that component's own networks and reach the others through the Node", where(p)))
			}
		}
	}
	for _, g := range e.Grants {
		used := false
		for _, p := range on.Protected {
			used = used || g.Matches(p.Component, p.Option)
		}
		if !used {
			res.Problems = append(res.Problems, fmt.Sprintf("declares %s, which no unit uses", whereGrant(g)))
		}
		if ok, needs := catalog.Support(g.Option); !ok {
			res.Problems = append(res.Problems, fmt.Sprintf("%s is not supported yet: Maître needs %s", whereGrant(g), needs))
		}
	}
	return res, nil
}

func render(ctx context.Context, e *catalog.Manifest, settings map[string]string, without []string, res *Result) (*services.CheckResult, error) {
	r, err := services.Check(ctx, catalog.SpecWithout(e, "", settings, "", without))
	if errors.Is(err, quadlet.ErrNoGenerator) {
		res.NoQuadlet = true
		err = nil
	}
	return r, err
}

func where(p services.ProtectedOption) string {
	if p.Component != "" {
		return p.Component + ": " + p.Option
	}
	return p.Option
}

func whereGrant(g catalog.Grant) string {
	if g.Component != "" {
		return g.Component + ": " + g.Option
	}
	return g.Option
}
