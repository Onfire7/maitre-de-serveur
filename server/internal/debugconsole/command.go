package debugconsole

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
)

// Usage is every command the Console has, which is every page it has: the
// screen is for watching one Node and these are for scripting several.
const Usage = `usage: maitre debug [flags first] [command]

With no command: the Debug Console, a screen for one Node.

  status                 who this Node is, what image it runs, how much room is left
  space                  the image store: how full, and which Service names each image
  services               every Service: state, health, restarts, network and image
  logs [source]          a journal: agent (the default), pool, caddy, health, kernel,
                         system, or service:<name>
  read <path>            a file on the Node, under its allowlist     (debug build only)
  run <command> [args]   a command on the Node, arguments passed through exactly
                                                                     (debug build only)
  prune                  delete images no Service of this Node names
  move-store             move the image store off the root partition (Services restart)

Flags come first, before the command: -addr, -identity, -trust, -lines,
-json, -drop-rollback, -store-dir, -demo.
-json prints each Node's answer as the protobuf JSON of the response, so a
script can read a field without parsing a table.`

// Options are the flags the commands take.
type Options struct {
	// Lines is how much journal `logs` asks for.
	Lines int
	// JSON prints the Node's answer instead of a table.
	JSON bool
	// DropRollback makes `prune` delete each Service's Rollback target too
	// (decision 320): the space comes back and Rollback does not.
	DropRollback bool
	// StoreDir is where `move-store` puts it; empty lets the Node choose.
	StoreDir string
}

// Command runs one of the Console's commands against src and writes the
// answer to out. Everything the screen can do is here, because the thing
// that ends an incident is often the same question asked of four Nodes at
// once.
func Command(ctx context.Context, src Source, out io.Writer, args []string, opt Options) error {
	if len(args) == 0 {
		return fmt.Errorf("%s", Usage)
	}
	// Go's flag package stops at the first argument that is not a flag, so
	// `space --json` would leave --json sitting unread in args and do the
	// wrong thing quietly. `run` is the exception: its arguments are the
	// Node's command's, dashes and all, and are passed through untouched.
	if args[0] != "run" {
		for _, a := range args[1:] {
			if strings.HasPrefix(a, "-") && a != "-" {
				return fmt.Errorf("flags go before the command: maitre debug %s %s, not maitre debug %s %s", a, args[0], args[0], a)
			}
		}
	}
	switch args[0] {
	case "status":
		return cmdStatus(ctx, src, out, opt)
	case "space", "images":
		return cmdSpace(ctx, src, out, opt)
	case "services":
		return cmdServices(ctx, src, out, opt)
	case "logs":
		return cmdLogs(ctx, src, out, args[1:], opt)
	case "read":
		if len(args) != 2 {
			return fmt.Errorf("usage: maitre debug read <path>")
		}
		return cmdRead(ctx, src, out, args[1], opt)
	case "run":
		if len(args) < 2 {
			return fmt.Errorf("usage: maitre debug run <command> [args...]")
		}
		return cmdRun(ctx, src, out, args[1], args[2:], opt)
	case "prune":
		return cmdPrune(ctx, src, out, opt)
	case "move-store":
		return cmdMoveStore(ctx, src, out, opt)
	}
	return fmt.Errorf("unknown debug command %q\n\n%s", args[0], Usage)
}

func cmdStatus(ctx context.Context, src Source, out io.Writer, opt Options) error {
	info, err := src.Info(ctx)
	if err != nil {
		return err
	}
	// The image status and the gauges are separate failures: a Node in dev
	// mode has no bootc, and a Node that just started has no readings.
	upd, updErr := src.UpdateStatus(ctx)
	mt, mtErr := src.Metrics(ctx)
	if opt.JSON {
		return writeJSONObject(out, []namedMessage{{"node", info}, {"update", upd}, {"metrics", mt}})
	}
	p := func(k string, v any) { fmt.Fprintf(out, "%-18s %v\n", k, v) }
	p("name", info.GetName())
	p("fleet", info.GetFleetId())
	p("node_id", info.GetNodeId())
	p("version", info.GetVersion())
	p("uptime", (time.Duration(info.GetUptimeSeconds()) * time.Second).String())
	p("dev_mode", info.GetDevMode())
	// Decision 323: said plainly wherever a Node is described, because the
	// risk is a debug image running in production unnoticed.
	if info.GetDebugBuild() {
		p("debug_build", "yes — this image answers read and run over the network")
	} else {
		p("debug_build", "no — read and run answer \"not a debug build\"")
	}
	p("peers", len(info.GetPeers()))
	if updErr != nil {
		p("image", "unavailable: "+updErr.Error())
	} else {
		p("booted", flatDeployment(upd.GetBooted()))
		if upd.GetStaged() != nil {
			p("staged", flatDeployment(upd.GetStaged()))
		}
		if upd.GetRollback() != nil {
			p("rollback", flatDeployment(upd.GetRollback()))
		}
		p("update_state", strings.ToLower(strings.TrimPrefix(upd.GetState().String(), "STATE_")))
		if upd.GetMessage() != "" {
			p("update_message", upd.GetMessage())
		}
		if upd.GetLastRollback() != "" {
			p("last_rollback", upd.GetLastRollback())
		}
	}
	if mtErr != nil {
		p("metrics", "unavailable: "+mtErr.Error())
		return nil
	}
	flat := func(k string, used, total uint64) {
		if total == 0 {
			p(k, "unknown")
			return
		}
		p(k, fmt.Sprintf("%.0f%% used, %s free of %s", 100*float64(used)/float64(total), humanBytes(total-used), humanBytes(total)))
	}
	// Root and the image store first: the gauges whose absence made the
	// incident of 2026-11-04 unreadable.
	flat("root", mt.GetSystemDiskUsedBytes(), mt.GetSystemDiskTotalBytes())
	flat("image_store", mt.GetImageStoreUsedBytes(), mt.GetImageStoreTotalBytes())
	p("image_store_dir", mt.GetImageStoreDir())
	flat("pool", mt.GetPoolUsedBytes(), mt.GetPoolTotalBytes())
	flat("memory", mt.GetMemoryUsedBytes(), mt.GetMemoryTotalBytes())
	p("cpu", fmt.Sprintf("%.1f%%  load %.2f", mt.GetCpuPercent(), mt.GetLoad_1M()))
	p("network", fmt.Sprintf("rx %s/s  tx %s/s", humanBytes(uint64(mt.GetNetRxBytesPerSec())), humanBytes(uint64(mt.GetNetTxBytesPerSec()))))
	return nil
}

func cmdSpace(ctx context.Context, src Source, out io.Writer, opt Options) error {
	res, err := src.Images(ctx)
	if err != nil {
		if TooOld(err) {
			return fmt.Errorf("this Node does not answer the image list (decision 326): update it, or it is a dev Node, which serves no Services")
		}
		return err
	}
	if opt.JSON {
		return writeJSON(out, res)
	}
	used := res.GetTotalBytes() - res.GetFreeBytes()
	pct := 0.0
	if res.GetTotalBytes() > 0 {
		pct = 100 * float64(used) / float64(res.GetTotalBytes())
	}
	fmt.Fprintf(out, "store      %s\n", res.GetStoreDir())
	fmt.Fprintf(out, "space      %.0f%% used, %s free of %s\n", pct, humanBytes(res.GetFreeBytes()), humanBytes(res.GetTotalBytes()))
	if res.GetFreeBytes() < PullFloor {
		fmt.Fprintf(out, "warning    below the %s this Node needs before it will pull an image\n", humanBytes(PullFloor))
	}
	var unnamed uint64
	for _, im := range res.GetImages() {
		if im.GetService() == "" {
			unnamed += im.GetSizeBytes()
		}
	}
	fmt.Fprintf(out, "images     %d, of which %s no Service names\n", len(res.GetImages()), humanBytes(unnamed))
	for _, im := range res.GetImages() {
		who := "-"
		if im.GetService() != "" {
			who = im.GetService()
			if im.GetRollbackTarget() {
				who += " (Rollback)"
			}
		}
		id := im.GetId()
		fmt.Fprintf(out, "  %-12s %10s  %-52s %s\n", id[:min(12, len(id))], humanBytes(im.GetSizeBytes()), im.GetReference(), who)
	}
	return nil
}

func cmdServices(ctx context.Context, src Source, out io.Writer, opt Options) error {
	rows, err := src.Services(ctx)
	if err != nil {
		return err
	}
	if opt.JSON {
		return writeJSON(out, &maitrev1.ListServicesResponse{Services: rows})
	}
	for _, s := range rows {
		health := "-"
		if s.GetState() == maitrev1.ServiceStatus_STATE_RUNNING {
			health = "not-healthy-yet"
			if s.GetHealthy() {
				health = "healthy"
			}
		}
		// Decision 317: a Service on a Tunnel reports no network of its
		// own, so the Console names the Tunnel rather than showing zero.
		net := "own"
		if s.GetNetworkSharedWith() != "" {
			net = "via " + s.GetNetworkSharedWith()
		}
		fmt.Fprintf(out, "%-16s %-9s %-16s restarts=%-4d %-18s %s\n", s.GetSpec().GetName(),
			strings.ToLower(strings.TrimPrefix(s.GetState().String(), "STATE_")), health, s.GetRestarts(), net, s.GetSpec().GetImage())
		if s.GetMessage() != "" {
			fmt.Fprintf(out, "  %s\n", s.GetMessage())
		}
	}
	return nil
}

func cmdLogs(ctx context.Context, src Source, out io.Writer, args []string, opt Options) error {
	source := ""
	if len(args) > 0 && args[0] != "agent" {
		source = args[0]
	}
	lines := opt.Lines
	if lines <= 0 {
		lines = 300
	}
	res, err := src.Logs(ctx, source, lines)
	if err != nil {
		return err
	}
	if opt.JSON {
		return writeJSON(out, res)
	}
	for _, l := range res.GetLines() {
		fmt.Fprintln(out, l)
	}
	// Said on stdout after the lines, because not knowing what else a Node
	// can show is how a diagnosis stalls.
	if len(res.GetSources()) > 0 {
		var ids []string
		for _, s := range res.GetSources() {
			ids = append(ids, s.GetId())
		}
		fmt.Fprintf(out, "\nsources: %s\n", strings.Join(ids, " "))
	}
	return nil
}

func cmdRead(ctx context.Context, src Source, out io.Writer, path string, opt Options) error {
	b, err := src.ReadFile(ctx, path)
	if err != nil {
		return err
	}
	if opt.JSON {
		return writeJSON(out, &maitrev1.DebugReadFileResponse{Content: b})
	}
	_, err = out.Write(b)
	return err
}

func cmdRun(ctx context.Context, src Source, out io.Writer, command string, args []string, opt Options) error {
	output, err := src.Exec(ctx, command, args)
	if opt.JSON {
		if jerr := writeJSON(out, &maitrev1.DebugExecResponse{Output: output}); jerr != nil {
			return jerr
		}
		return err
	}
	// The output is the diagnosis even when the command failed, so it is
	// printed either way and the failure is still the exit status.
	if output != "" {
		fmt.Fprint(out, output)
	}
	return err
}

func cmdPrune(ctx context.Context, src Source, out io.Writer, opt Options) error {
	// Decision 316: a Node has no terminal, so this is the only way to get
	// the image store's space back.
	res, err := src.Prune(ctx, !opt.DropRollback)
	if err != nil {
		if TooOld(err) {
			return fmt.Errorf("this Node does not answer prune (decision 316): update it, or it is a dev Node, which serves no Services")
		}
		return err
	}
	if opt.JSON {
		return writeJSON(out, res)
	}
	fmt.Fprintf(out, "store      %s\n", res.GetStoreDir())
	fmt.Fprintf(out, "free       %s before, %s after: %s back\n",
		humanBytes(res.GetFreeBeforeBytes()), humanBytes(res.GetFreeAfterBytes()),
		humanBytes(res.GetFreeAfterBytes()-res.GetFreeBeforeBytes()))
	fmt.Fprintf(out, "kept       %d image(s) this Node's Services name\n", res.GetKept())
	fmt.Fprintf(out, "removed    %d\n", len(res.GetRemoved()))
	for _, r := range res.GetRemoved() {
		fmt.Fprintf(out, "  %s\n", r)
	}
	return nil
}

func cmdMoveStore(ctx context.Context, src Source, out io.Writer, opt Options) error {
	// Decision 319: off the root partition, which was never sized for it.
	// The Services of this Node stop for the copy and start again after.
	res, err := src.MoveStore(ctx, opt.StoreDir)
	if err != nil {
		if TooOld(err) {
			return fmt.Errorf("this Node does not answer the image store move (decision 319): update it, or it is a dev Node, which serves no Services")
		}
		return err
	}
	if opt.JSON {
		return writeJSON(out, res)
	}
	fmt.Fprintf(out, "store      %s -> %s\n", res.GetFrom(), res.GetTo())
	fmt.Fprintf(out, "moved      %s\n", humanBytes(res.GetMovedBytes()))
	fmt.Fprintf(out, "root       %s free before, %s after: %s back\n",
		humanBytes(res.GetRootFreeBeforeBytes()), humanBytes(res.GetRootFreeAfterBytes()),
		humanBytes(res.GetRootFreeAfterBytes()-res.GetRootFreeBeforeBytes()))
	if len(res.GetServices()) > 0 {
		fmt.Fprintf(out, "restarted  %d: %s\n", len(res.GetServices()), strings.Join(res.GetServices(), ", "))
	}
	return nil
}

func flatDeployment(d *maitrev1.ImageDeployment) string {
	if d == nil {
		return "none"
	}
	out := d.GetImage()
	if v := d.GetVersion(); v != "" {
		out += " " + v
	}
	if g := d.GetDigest(); g != "" {
		out += " " + shortID(g)
	}
	return out
}

var jsonOut = protojson.MarshalOptions{Multiline: true, Indent: "  "}

func writeJSON(out io.Writer, m proto.Message) error {
	b, err := jsonOut.Marshal(m)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, string(b))
	return err
}

type namedMessage struct {
	name string
	msg  proto.Message
}

// writeJSONObject prints several answers as one object, so that `status`
// has a single shape a script can read.
func writeJSONObject(out io.Writer, parts []namedMessage) error {
	obj := map[string]json.RawMessage{}
	for _, p := range parts {
		b, err := jsonOut.Marshal(p.msg)
		if err != nil {
			return err
		}
		obj[p.name] = json.RawMessage(b)
	}
	b, err := json.MarshalIndent(obj, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, string(b))
	return err
}
