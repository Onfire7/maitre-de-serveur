package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/Onfire7/maitre-de-serveur/server/internal/catalog"
	"github.com/Onfire7/maitre-de-serveur/server/internal/services"
	"github.com/Onfire7/maitre-de-serveur/server/internal/storage"
)

// runCatalogIndex is `maitre catalog-index <entries dir>`: validates every
// Catalog entry and prints index.json. The image build and the Catalog
// repository's CI both use it.
func runCatalogIndex(args []string) error {
	fs := flag.NewFlagSet("catalog-index", flag.ExitOnError)
	out := fs.String("out", "", "write here instead of stdout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: maitre catalog-index [--out index.json] <entries dir>")
	}
	idx, err := catalog.LoadDir(fs.Arg(0))
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if *out == "" {
		_, err = os.Stdout.Write(raw)
		return err
	}
	return os.WriteFile(*out, raw, 0o644)
}

// runCatalogCheck is `maitre catalog-check <entries dir> [id...]`: renders
// each entry (or the ones named) as a Node installs it and runs Podman's
// Quadlet over the units, the Catalog's second check (plan C8). An entry
// that needs a grant fails too: the Catalog's entries run inside Maître's
// limits. CI runs it inside the maitre-server image, which has Quadlet.
func runCatalogCheck(args []string) error {
	fs := flag.NewFlagSet("catalog-check", flag.ExitOnError)
	units := fs.Bool("units", false, "print the unit files each entry renders to")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("usage: maitre catalog-check [--units] <entries dir> [id...]")
	}
	idx, err := catalog.LoadDir(fs.Arg(0))
	if err != nil {
		return err
	}
	ids := fs.Args()[1:]
	if len(ids) == 0 {
		for _, e := range idx.Entries {
			ids = append(ids, e.ID)
		}
	}
	failed := 0
	for _, id := range ids {
		e := idx.Find(id)
		if e == nil {
			return fmt.Errorf("no entry %q", id)
		}
		res, err := services.Check(context.Background(), catalog.SpecFor(e, "", nil, ""))
		switch {
		case err != nil:
			failed++
			fmt.Printf("FAIL %s: %s\n", id, strings.TrimPrefix(err.Error(), id+": "))
		case len(res.Protected) > 0:
			failed++
			fmt.Printf("FAIL %s needs grants:\n", id)
			for _, p := range res.Protected {
				fmt.Printf("  %s %s\n", p.Component, p.Option)
			}
		default:
			fmt.Printf("ok   %s\n", id)
		}
		if *units && res != nil {
			names := make([]string, 0, len(res.Units))
			for n := range res.Units {
				names = append(names, n)
			}
			sort.Strings(names)
			for _, n := range names {
				fmt.Printf("--- %s\n%s", n, res.Units[n])
			}
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d entries failed", failed, len(ids))
	}
	return nil
}

// runNamesIndex is `maitre names-index --out names.json`: the shared Node
// and Drive name lists by letter, bundled into the Console by `make gen`.
func runNamesIndex(args []string) error {
	fs := flag.NewFlagSet("names-index", flag.ExitOnError)
	out := fs.String("out", "", "write here instead of stdout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(storage.AllNames(), "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if *out == "" {
		_, err = os.Stdout.Write(raw)
		return err
	}
	return os.WriteFile(*out, raw, 0o644)
}
