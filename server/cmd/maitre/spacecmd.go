package main

import (
	"context"
	"flag"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"google.golang.org/protobuf/proto"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1/maitrev1connect"
	"github.com/Onfire7/maitre-de-serveur/server/internal/identity"
	"github.com/Onfire7/maitre-de-serveur/server/internal/mtls"
	"github.com/Onfire7/maitre-de-serveur/server/internal/storage"
	"github.com/Onfire7/maitre-de-serveur/server/internal/trust"
)

// runSpace is `maitre space list|create`: Storage Spaces from a terminal
// (decision 123: a Fleet has none until one is created, and Services need
// one marked default). Used by the boot test; handy without a Console.
func runSpace(args []string) error {
	fs := flag.NewFlagSet("space", flag.ExitOnError)
	idDir := fs.String("identity", filepath.Join(defaultState(), "device"), "directory holding the caller's identity")
	trustDir := fs.String("trust", filepath.Join(defaultState(), "device", "trusted"), "directory of *.pub files for Nodes to trust")
	addr := fs.String("addr", "127.0.0.1:7443", "Node address")
	id := fs.String("id", "", "create: Space id (lower-case letters, digits, dashes)")
	name := fs.String("name", "", "create: display name (default: the id)")
	tags := fs.String("tags", "", "create: required Drive tags, comma-separated (empty: every Drive)")
	placement := fs.String("placement", storage.PlacementLocalFirst, "create: local_first (most_free, local Drives first), most_free, most_free_percent, least_free or least_free_percent")
	span := fs.Bool("span", false, "create: one Pool across Nodes")
	exclusive := fs.Bool("exclusive", false, "create: member Drives serve this Space only")
	def := fs.Bool("default", false, "create: new Service volumes land here")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: maitre space [flags] list|create")
	}
	ident, err := identity.Load(*idDir)
	if err != nil {
		return err
	}
	list := trust.New()
	if err := list.LoadDir(*trustDir); err != nil {
		return err
	}
	c := maitrev1connect.NewNodeServiceClient(connect.NewClient(connecthttp.NewTransport(NewHTTPClient(mtls.ClientConfig(ident, list)), "https://"+*addr)))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	switch fs.Arg(0) {
	case "list":
		res, err := c.ListRecords(ctx, &maitrev1.ListRecordsRequest{Kind: storage.KindSpace})
		if err != nil {
			return err
		}
		for _, r := range res.GetRecords() {
			if r.GetDeleted() {
				continue
			}
			var s maitrev1.Space
			if err := proto.Unmarshal(r.GetBody(), &s); err != nil {
				return err
			}
			var flags []string
			if s.GetDefaultForNew() {
				flags = append(flags, "default")
			}
			if s.GetSpanNodes() {
				flags = append(flags, "spans")
			}
			if s.GetExclusive() {
				flags = append(flags, "exclusive")
			}
			rule := strings.Join(s.GetRequireTags(), "+")
			if rule == "" {
				rule = "every Drive"
			}
			fmt.Printf("%-16s %-20s %-18s %-20s %s\n", s.GetSpaceId(), s.GetName(), s.GetPlacement(), rule, strings.Join(flags, ","))
		}
	case "create":
		if *name == "" {
			*name = *id
		}
		var req []string
		for _, t := range strings.Split(*tags, ",") {
			if t = strings.TrimSpace(t); t != "" {
				req = append(req, t)
			}
		}
		s := &maitrev1.Space{SpaceId: *id, Name: *name, Placement: *placement, RequireTags: req, SpanNodes: *span, Exclusive: *exclusive, DefaultForNew: *def, CreatedUnixMs: time.Now().UnixMilli()}
		if err := storage.ValidateSpace(s); err != nil {
			return err
		}
		body, _ := proto.Marshal(s)
		if _, err := c.PutRecord(ctx, &maitrev1.PutRecordRequest{Record: signed(ident, &maitrev1.Record{Kind: storage.KindSpace, Id: s.GetSpaceId(), Body: body})}); err != nil {
			return err
		}
		fmt.Printf("created Space %s\n", s.GetSpaceId())
	default:
		return fmt.Errorf("unknown space command %q", fs.Arg(0))
	}
	return nil
}
