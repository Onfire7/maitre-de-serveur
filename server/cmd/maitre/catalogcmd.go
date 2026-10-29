package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Onfire7/maitre-de-serveur/server/internal/catalog"
	"github.com/Onfire7/maitre-de-serveur/server/internal/catalogsync"
	"github.com/Onfire7/maitre-de-serveur/server/internal/convert"
	"github.com/Onfire7/maitre-de-serveur/server/internal/icons"
	"github.com/Onfire7/maitre-de-serveur/server/internal/services"
	"github.com/Onfire7/maitre-de-serveur/server/internal/storage"
)

// runCatalogIndex is `maitre catalog-index <entries dir>`: validates every
// Catalog entry and prints index.json. The image build and the Catalog
// repository's CI both use it.
func runCatalogIndex(args []string) error {
	fs := flag.NewFlagSet("catalog-index", flag.ExitOnError)
	out := fs.String("out", "", "write here instead of stdout")
	lenient := fs.Bool("lenient", false, "a user's Catalog: list the entries that do not load as problems instead of failing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: maitre catalog-index [--out index.json] [--lenient] <entries dir>")
	}
	var v any
	if *lenient {
		idx, problems, err := catalog.LoadDirLenient(fs.Arg(0))
		if err != nil {
			return err
		}
		v = struct {
			*catalog.Index
			Problems []catalog.Problem `json:"problems,omitempty"`
		}{idx, problems}
	} else {
		idx, err := catalog.LoadDir(fs.Arg(0))
		if err != nil {
			return err
		}
		v = idx
	}
	raw, err := json.MarshalIndent(v, "", "  ")
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
		case len(e.Unfinished) > 0:
			failed++
			fmt.Printf("FAIL %s is unfinished:\n", id)
			for _, u := range e.Unfinished {
				fmt.Printf("  %s\n", u)
			}
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

// runCatalog is `maitre catalog add <catalog folder> <compose file or
// CasaOS app folder>`: converts a Compose file into a bundle in the
// Catalog's entries (plan C10), printing the report at once and saving it
// as CONVERSION.md. With --json the report and entry are printed as JSON
// for the Console.
func runCatalog(args []string) error {
	if len(args) > 0 && args[0] == "icon" {
		return runCatalogIcon(args[1:])
	}
	if len(args) > 0 && args[0] == "sync" {
		return runCatalogSync(args[1:])
	}
	if len(args) == 0 || args[0] != "add" {
		return fmt.Errorf("usage: maitre catalog add [--id id] [--url url] [--licence spdx] [--icon name] [--replace] [--json] <catalog folder> <compose file or folder>\n       maitre catalog icon <entry dir> [name in the icon packs]")
	}
	fs := flag.NewFlagSet("catalog add", flag.ExitOnError)
	id := fs.String("id", "", "the entry's id (default: the Compose project's name)")
	url := fs.String("url", "", "where the Compose file was published")
	licence := fs.String("licence", "", "the Compose file's licence, as an SPDX id")
	replace := fs.Bool("replace", false, "convert over an existing entry")
	asJSON := fs.Bool("json", false, "print the report and entry as JSON")
	iconName := fs.String("icon", "", "the app's name in the icon packs (default: the entry's id; - for none)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return fmt.Errorf("usage: maitre catalog add [flags] <catalog folder> <compose file or folder>")
	}
	res, err := convert.Convert(context.Background(), convert.Options{Compose: fs.Arg(1), ID: *id, URL: *url, Licence: *licence})
	if err != nil {
		return err
	}
	written := ""
	if res.Files != nil {
		if written, err = convert.Write(fs.Arg(0), res, *replace); err != nil {
			return err
		}
		// The icon, copied from a pack; a missing one is only noted.
		if slug := *iconName; slug != "-" {
			if slug == "" {
				slug = res.Entry.ID
			}
			if icon, err := icons.Default().Fetch(context.Background(), slug); err != nil {
				res.Report.Notes = append(res.Report.Notes, fmt.Sprintf("No icon copied (%s: %v); add one with `maitre catalog icon %s <name>`", slug, err, written))
			} else if err := copyIcon(written, icon); err != nil {
				return err
			}
		}
	}
	if *asJSON {
		out, _ := json.MarshalIndent(map[string]any{"report": res.Report, "entry": res.Entry, "dir": written}, "", "  ")
		fmt.Println(string(out))
	} else {
		name := fs.Arg(1)
		if res.Entry != nil {
			name = res.Entry.Name
		}
		fmt.Print(res.Report.Markdown(name, fs.Arg(1)))
		if written != "" {
			fmt.Printf("\nWritten to %s", written)
			if len(res.Entry.Unfinished) > 0 {
				fmt.Printf(", unfinished: %d to decide", len(res.Entry.Unfinished))
			}
			fmt.Println(".")
		}
	}
	if res.Files == nil {
		return fmt.Errorf("not converted: %d to fix first", len(res.Report.MustFix))
	}
	return nil
}

// runCatalogNotice is `maitre catalog-notice --out NOTICE <entries dir>`:
// the Catalog's NOTICE, from its entries' sources (plan C11, C12).
func runCatalogNotice(args []string) error {
	fs := flag.NewFlagSet("catalog-notice", flag.ExitOnError)
	out := fs.String("out", "", "write here instead of stdout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: maitre catalog-notice [--out NOTICE] <entries dir>")
	}
	idx, err := catalog.LoadDir(fs.Arg(0))
	if err != nil {
		return err
	}
	if *out == "" {
		_, err = fmt.Print(catalog.Notice(idx))
		return err
	}
	return os.WriteFile(*out, []byte(catalog.Notice(idx)), 0o644)
}

// runCatalogIcon is `maitre catalog icon <entry dir> [name]`: copies the
// app's icon from an icon pack into the entry and records where it came
// from (plan C12). name is the app's name in the packs; the entry's id by
// default.
func runCatalogIcon(args []string) error {
	if len(args) < 1 || len(args) > 2 {
		return fmt.Errorf("usage: maitre catalog icon <entry dir> [name in the icon packs]")
	}
	dir := args[0]
	slug := filepath.Base(filepath.Clean(dir))
	if len(args) == 2 {
		slug = args[1]
	}
	icon, err := icons.Default().Fetch(context.Background(), slug)
	if err != nil {
		return fmt.Errorf("%s: %w", slug, err)
	}
	if err := copyIcon(dir, icon); err != nil {
		return err
	}
	fmt.Printf("%s: %s from %s (%s)\n", dir, icon.File, icon.Credit.Pack, icon.Credit.Licence)
	return nil
}

// copyIcon writes an icon into an entry, replacing any other icon file.
func copyIcon(dir string, icon *icons.Icon) error {
	for _, old := range []string{"icon.svg", "icon.png"} {
		if old != icon.File {
			_ = os.Remove(filepath.Join(dir, old))
		}
	}
	if err := os.WriteFile(filepath.Join(dir, icon.File), icon.Body, 0o644); err != nil {
		return err
	}
	return catalog.SetIcon(dir, icon.File, icon.Credit)
}

// runCatalogSync is `maitre catalog sync [--branch b] <git url> <dir>`: the
// Console's clone of a git Catalog brought to the head of its branch (plan
// C9). Prints the commit.
func runCatalogSync(args []string) error {
	fs := flag.NewFlagSet("catalog sync", flag.ExitOnError)
	branch := fs.String("branch", "", "the branch to follow (default: the repository's own)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return fmt.Errorf("usage: maitre catalog sync [--branch b] <git url> <dir>")
	}
	commit, err := catalogsync.Sync(context.Background(), fs.Arg(0), *branch, fs.Arg(1))
	if err != nil {
		return err
	}
	fmt.Println(commit)
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
