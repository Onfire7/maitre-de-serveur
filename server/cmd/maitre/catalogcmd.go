package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/Onfire7/maitre-de-serveur/server/internal/catalog"
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
