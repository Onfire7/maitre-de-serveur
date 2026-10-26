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

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1/maitrev1connect"
	"github.com/Onfire7/maitre-de-serveur/server/internal/identity"
	"github.com/Onfire7/maitre-de-serveur/server/internal/mtls"
	"github.com/Onfire7/maitre-de-serveur/server/internal/trust"
)

// runFiles is `maitre files ls|du|mv|jobs|cancel`: the file manager's back
// end (decision 122) from a terminal, for checking a Node.
func runFiles(args []string) error {
	fs := flag.NewFlagSet("files", flag.ExitOnError)
	idDir := fs.String("identity", filepath.Join(defaultState(), "device"), "directory holding the caller's identity")
	trustDir := fs.String("trust", filepath.Join(defaultState(), "device", "trusted"), "directory of *.pub files for Nodes to trust")
	addr := fs.String("addr", "127.0.0.1:7443", "Node address")
	scope := fs.String("scope", "", "space:<id> or drive:<id>")
	path := fs.String("path", "/", "path inside the scope")
	toSpace := fs.String("to-space", "", "mv: destination Space")
	toPath := fs.String("to-path", "", "mv: destination path (default: same)")
	toNode := fs.String("to-node", "", "mv: destination Node id")
	toDrive := fs.String("to-drive", "", "mv: destination Drive id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: maitre files [flags] ls|du|mv|jobs|cancel <job>")
	}
	id, err := identity.Load(*idDir)
	if err != nil {
		return err
	}
	list := trust.New()
	if err := list.LoadDir(*trustDir); err != nil {
		return err
	}
	c := maitrev1connect.NewFilesServiceClient(connect.NewClient(connecthttp.NewTransport(NewHTTPClient(mtls.ClientConfig(id, list)), "https://"+*addr)))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sc := &maitrev1.Scope{}
	if k, v, ok := strings.Cut(*scope, ":"); ok {
		switch k {
		case "space":
			sc.SpaceId = v
		case "drive":
			sc.DriveId = v
		}
	}
	switch fs.Arg(0) {
	case "ls":
		res, err := c.ListDir(ctx, &maitrev1.ListDirRequest{Scope: sc, Path: *path})
		if err != nil {
			return err
		}
		for _, e := range res.GetEntries() {
			kind := "file"
			if e.GetDir() {
				kind = "dir "
			}
			var locs []string
			for _, l := range e.GetLocations() {
				locs = append(locs, l.GetNodeId()[:8]+"/"+short(l.GetDriveId()))
			}
			fmt.Printf("%s %12d  %-40s %s\n", kind, e.GetSizeBytes(), e.GetName(), strings.Join(locs, ","))
		}
	case "du":
		for {
			res, err := c.Usage(ctx, &maitrev1.UsageRequest{Scope: sc, Path: *path})
			if err != nil {
				return err
			}
			if !res.GetInProgress() {
				fmt.Printf("%d bytes in %d files\n", res.GetBytes(), res.GetFiles())
				return nil
			}
			time.Sleep(500 * time.Millisecond)
		}
	case "mv":
		res, err := c.Move(ctx, &maitrev1.MoveRequest{From: sc, Path: *path, ToSpace: *toSpace, ToPath: *toPath, ToNodeId: *toNode, ToDriveId: *toDrive})
		if err != nil {
			return err
		}
		fmt.Println("queued", res.GetJobId())
	case "jobs":
		res, err := c.ListJobs(ctx, &maitrev1.ListJobsRequest{})
		if err != nil {
			return err
		}
		for _, j := range res.GetJobs() {
			fmt.Printf("%s %-9s %d/%d %s %s\n", j.GetJobId(), j.GetState(), j.GetBytesDone(), j.GetBytesTotal(), j.GetCurrent(), j.GetMessage())
		}
	case "cancel":
		if _, err := c.CancelJob(ctx, &maitrev1.CancelJobRequest{JobId: *path}); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown files command %q", fs.Arg(0))
	}
	return nil
}

func short(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}
