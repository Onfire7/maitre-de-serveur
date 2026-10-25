package main

import (
	"context"
	"flag"
	"fmt"
	"path/filepath"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1/maitrev1connect"
	"github.com/Onfire7/maitre-de-serveur/server/internal/identity"
	"github.com/Onfire7/maitre-de-serveur/server/internal/mtls"
	"github.com/Onfire7/maitre-de-serveur/server/internal/trust"
)

// runRecord is `maitre record <list|get|put|delete>`: raw access to the
// Fleet database through a Node, for tests and debugging of the sync layer.
func runRecord(args []string) error {
	fs := flag.NewFlagSet("record", flag.ExitOnError)
	idDir := fs.String("identity", filepath.Join(defaultState(), "device"), "directory holding the caller's identity")
	trustDir := fs.String("trust", filepath.Join(defaultState(), "device", "trusted"), "directory of *.pub files for Nodes to trust")
	addr := fs.String("addr", fmt.Sprintf("127.0.0.1:%d", port), "Node address")
	kind := fs.String("kind", "", "record kind")
	rid := fs.String("id", "", "record id")
	body := fs.String("body", "", "put: body as text")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("usage: maitre record [flags] <list|get|put|delete>")
	}
	id, err := identity.Load(*idDir)
	if err != nil {
		return err
	}
	list := trust.New()
	if err := list.LoadDir(*trustDir); err != nil {
		return err
	}
	transport := connecthttp.NewTransport(NewHTTPClient(mtls.ClientConfig(id, list)), "https://"+*addr)
	c := maitrev1connect.NewNodeServiceClient(connect.NewClient(transport))
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	switch fs.Arg(0) {
	case "list":
		res, err := c.ListRecords(ctx, &maitrev1.ListRecordsRequest{Kind: *kind})
		if err != nil {
			return err
		}
		for _, r := range res.GetRecords() {
			del := ""
			if r.GetDeleted() {
				del = "  (deleted)"
			}
			fmt.Printf("%-16s %-40s owner=%s v%d%s\n", r.GetKind(), r.GetId(), r.GetOwnerNodeId()[:8], r.GetVersion(), del)
		}
	case "get":
		res, err := c.ListRecords(ctx, &maitrev1.ListRecordsRequest{Kind: *kind})
		if err != nil {
			return err
		}
		for _, r := range res.GetRecords() {
			if r.GetId() == *rid {
				fmt.Printf("owner=%s version=%d deleted=%v\n%s\n", r.GetOwnerNodeId(), r.GetVersion(), r.GetDeleted(), r.GetBody())
				return nil
			}
		}
		return fmt.Errorf("no record %s/%s", *kind, *rid)
	case "put", "delete":
		res, err := c.PutRecord(ctx, &maitrev1.PutRecordRequest{Record: &maitrev1.Record{Kind: *kind, Id: *rid, Body: []byte(*body), Deleted: fs.Arg(0) == "delete"}})
		if err != nil {
			return err
		}
		if res.GetQueued() {
			fmt.Println("queued: the owner is offline; delivered when it returns")
			return nil
		}
		fmt.Printf("applied: owner=%s version=%d\n", res.GetRecord().GetOwnerNodeId(), res.GetRecord().GetVersion())
	default:
		return fmt.Errorf("unknown record command %q", fs.Arg(0))
	}
	return nil
}
