package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"golang.org/x/term"
	"google.golang.org/protobuf/proto"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1/maitrev1connect"
	"github.com/Onfire7/maitre-de-serveur/server/internal/authz"
	"github.com/Onfire7/maitre-de-serveur/server/internal/backup"
	"github.com/Onfire7/maitre-de-serveur/server/internal/caddy"
	"github.com/Onfire7/maitre-de-serveur/server/internal/catalog"
	"github.com/Onfire7/maitre-de-serveur/server/internal/copies"
	"github.com/Onfire7/maitre-de-serveur/server/internal/depot"
	"github.com/Onfire7/maitre-de-serveur/server/internal/direct"
	"github.com/Onfire7/maitre-de-serveur/server/internal/drain"
	"github.com/Onfire7/maitre-de-serveur/server/internal/entry"
	"github.com/Onfire7/maitre-de-serveur/server/internal/files"
	"github.com/Onfire7/maitre-de-serveur/server/internal/filesservice"
	"github.com/Onfire7/maitre-de-serveur/server/internal/gate"
	"github.com/Onfire7/maitre-de-serveur/server/internal/history"
	"github.com/Onfire7/maitre-de-serveur/server/internal/identity"
	"github.com/Onfire7/maitre-de-serveur/server/internal/installer"
	"github.com/Onfire7/maitre-de-serveur/server/internal/mdns"
	"github.com/Onfire7/maitre-de-serveur/server/internal/metrics"
	"github.com/Onfire7/maitre-de-serveur/server/internal/mtls"
	"github.com/Onfire7/maitre-de-serveur/server/internal/nodeservice"
	"github.com/Onfire7/maitre-de-serveur/server/internal/parity"
	"github.com/Onfire7/maitre-de-serveur/server/internal/peer"
	"github.com/Onfire7/maitre-de-serveur/server/internal/peers"
	"github.com/Onfire7/maitre-de-serveur/server/internal/pool"
	"github.com/Onfire7/maitre-de-serveur/server/internal/portmap"
	"github.com/Onfire7/maitre-de-serveur/server/internal/recoverymode"
	"github.com/Onfire7/maitre-de-serveur/server/internal/remote"
	"github.com/Onfire7/maitre-de-serveur/server/internal/rollout"
	"github.com/Onfire7/maitre-de-serveur/server/internal/services"
	"github.com/Onfire7/maitre-de-serveur/server/internal/serviceservice"
	"github.com/Onfire7/maitre-de-serveur/server/internal/storage"
	"github.com/Onfire7/maitre-de-serveur/server/internal/storageservice"
	"github.com/Onfire7/maitre-de-serveur/server/internal/store"
	"github.com/Onfire7/maitre-de-serveur/server/internal/svcproxy"
	"github.com/Onfire7/maitre-de-serveur/server/internal/tailnet"
	"github.com/Onfire7/maitre-de-serveur/server/internal/trust"
	"github.com/Onfire7/maitre-de-serveur/server/internal/tui"
	"github.com/Onfire7/maitre-de-serveur/server/internal/update"
)

// Port every Node and Installer listens on.
const port = 7443

// runAgent is `maitre agent`: the service on an installed Node. State lives
// on the config partition, mounted at --state (/var/lib/maitre on the image).
func runAgent(args []string) error {
	fs := flag.NewFlagSet("agent", flag.ExitOnError)
	state := fs.String("state", "/var/lib/maitre", "config partition mount point")
	listen := fs.String("listen", fmt.Sprintf("0.0.0.0:%d", port), "address to listen on")
	noMDNS := fs.Bool("no-mdns", false, "do not advertise on mDNS")
	noTailscale := fs.Bool("no-tailscale", false, "do not join the Fleet's Tailscale network even if configured")
	catalogPath := fs.String("catalog", "/usr/share/maitre/catalog.json", "Catalog index baked into the image")
	noScreen := fs.Bool("no-screen", false, "do not draw the status page on stdout")
	unitsDir := fs.String("units", "/etc/containers/systemd", "Quadlet units directory")
	poolsDir := fs.String("pools", pool.PoolsDir, "directory holding one Pool per Storage Space")
	caddyAdmin := fs.String("caddy", "127.0.0.1:2019", "Caddy admin address; empty disables")
	advertise := fs.String("advertise", "", "host:port peers should reach this Node at (default: every address, port from --listen)")
	recoveryPort := fs.Int("recovery-port", recoverymode.Port, "port of the recovery listener while a recovery code is pending")
	if err := fs.Parse(args); err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	id, err := identity.Load(filepath.Join(*state, "node"))
	if err != nil {
		return fmt.Errorf("this machine has not been provisioned (no identity in %s): %w", *state, err)
	}
	st, err := store.Open(filepath.Join(*state, "fleet.db"), id.Fingerprint())
	if err != nil {
		return err
	}
	defer st.Close()
	// Decision 189: this Node signs what it writes, and takes a Record
	// from a peer only when the right key signed it.
	st.SignWith(id)
	policy := authz.New(st, id.Fingerprint())
	st.Admit = policy.Admit
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fleet := &maitrev1.Fleet{}
	fleets, err := store.ListLive(ctx, st, store.KindFleet, func() *maitrev1.Fleet { return &maitrev1.Fleet{} })
	if err != nil {
		return err
	}
	if len(fleets) == 1 {
		fleet = fleets[0]
	}
	var self maitrev1.Node
	if _, err := st.Get(ctx, store.KindNode, id.Fingerprint(), &self); err != nil {
		return fmt.Errorf("own node record: %w", err)
	}

	list := trust.New()
	// Trust list: devices that may manage this Node, and the Fleet's other
	// Nodes (Milestone 3). Refreshed whenever those records change.
	refreshTrust := func() {
		devices, limited, nodes, err := peers.TrustedRoles(ctx, st, id.Fingerprint())
		if err != nil {
			log.Error("load trust list", "err", err)
			return
		}
		list.ReplaceRoles(devices, limited, nodes)
	}
	refreshTrust()
	// Decision 189: our own Records from before signing get signed, so
	// peers keep taking their changes. Device-signed kinds stay as they
	// are here until a Trusted Device saves them again.
	if recs, err := st.ResignOwn(ctx, func(kind string) bool { return !authz.DeviceOnly(kind) }); err != nil {
		log.Warn("sign own records", "err", err)
	} else if len(recs) > 0 {
		log.Info("signed own records from before decision 189", "count", len(recs))
	}
	log.Info("maitre agent", "node_id", id.Fingerprint(), "name", self.GetName(), "fleet", fleet.GetName(), "trusted_devices", list.Len())

	// Decision 101: a reboot into recovery (chosen at the keyboard) runs
	// with networking off, shows a one-time code, and restarts.
	if recoverymode.Requested(*state) {
		return runRecoveryScreen(ctx, log, *state, self.GetName(), fleet.GetName())
	}

	ensureHostname(log, hostLabel(self.GetName()))

	// Peers (ADR 0003): our own Node record carries the addresses peers
	// should try; mDNS keeps them fresh on the LAN.
	pm := peers.New(st, id, list, log)
	addrs := []string{}
	if *advertise != "" {
		addrs = append(addrs, *advertise)
	} else {
		for _, a := range installer.Addresses() {
			addrs = append(addrs, net.JoinHostPort(a, fmt.Sprint(listenPort(*listen))))
		}
	}
	// Decision 188: the certificate peers trust for NFS over TLS, published
	// in our Node record and written where tlshd reads it.
	nfsCert, nfsSig, nfsKey, err := id.NFSCredentials()
	if err != nil {
		return fmt.Errorf("nfs certificate: %w", err)
	}
	if err := storage.WriteTLSIdentity(nfsCert, nfsKey); err != nil {
		log.Warn("nfs tls identity", "err", err)
	}
	if (len(addrs) > 0 && !slices.Equal(addrs, self.GetAddresses())) || self.GetNfsCertificatePem() != string(nfsCert) || !bytes.Equal(self.GetNfsCertificateSignature(), nfsSig) {
		if len(addrs) > 0 {
			self.Addresses = addrs
		}
		self.NfsCertificatePem = string(nfsCert)
		self.NfsCertificateSignature = nfsSig
		if _, err := st.Put(ctx, store.KindNode, id.Fingerprint(), &self, false); err != nil {
			log.Warn("own node record", "err", err)
		}
	}
	if err := pm.Refresh(ctx); err != nil {
		log.Warn("peers", "err", err)
	}
	// editSelf changes this Node's own record from what is stored now (not
	// a copy read at start, which would undo other changes), then pushes it.
	var selfMu sync.Mutex
	editSelf := func(change func(*maitrev1.Node) bool) {
		selfMu.Lock()
		defer selfMu.Unlock()
		var cur maitrev1.Node
		if _, err := st.Get(ctx, store.KindNode, id.Fingerprint(), &cur); err != nil {
			log.Warn("own node record", "err", err)
			return
		}
		if !change(&cur) {
			return
		}
		rec, err := st.Put(ctx, store.KindNode, id.Fingerprint(), &cur, false)
		if err != nil {
			log.Warn("own node record", "err", err)
			return
		}
		proto.Reset(&self)
		proto.Merge(&self, &cur)
		pm.Push(context.WithoutCancel(ctx), rec)
	}
	// Decision 104: one letter per Node; its Drives are named after it.
	assignLetter := func() {
		if l, wrote, err := storage.AssignLetter(ctx, st); err != nil {
			log.Warn("node letter", "err", err)
		} else if wrote {
			log.Info("node letter", "letter", l)
			if rec, err := st.Get(ctx, store.KindNode, id.Fingerprint(), &self); err == nil {
				pm.Push(context.WithoutCancel(ctx), rec)
			}
		}
	}
	assignLetter()

	svc := nodeservice.New(id, nodeservice.Info{Name: self.GetName(), FleetID: fleet.GetFleetId(), Version: version}, st)
	svc.Peers = pm
	svc.Limited = list.Limited
	svc.Authz = policy
	svc.OnChange = func(r *maitrev1.Record) {
		switch r.GetKind() {
		case store.KindTrustedDevice:
			refreshTrust()
		case store.KindNode:
			refreshTrust()
			_ = pm.Refresh(ctx)
		}
	}
	// Decision 102: after any change, copy the config partition to every
	// Drive (debounced), so a reinstalled machine can restore itself.
	backupNow := make(chan struct{}, 1)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-backupNow:
			}
			time.Sleep(5 * time.Second)
			drives, _ := filepath.Glob(filepath.Join(pool.DrivesDir, "*"))
			for _, d := range drives {
				if !isMountpoint(d) {
					continue
				}
				if err := backup.Write(ctx, st, *state, d, backup.Meta{NodeID: id.Fingerprint(), NodeName: self.GetName(), FleetID: fleet.GetFleetId(), FleetName: fleet.GetName()}); err != nil {
					log.Warn("config backup", "drive", d, "err", err)
				}
			}
		}
	}()
	requestBackup := func() {
		select {
		case backupNow <- struct{}{}:
		default:
		}
	}
	requestBackup()
	prevOnChange := svc.OnChange
	svc.OnChange = func(r *maitrev1.Record) {
		prevOnChange(r)
		requestBackup()
	}
	pm.OnApplied = func() {
		refreshTrust()
		_ = pm.Refresh(ctx)
		assignLetter()
		requestBackup()
	}
	go pm.Run(ctx)
	go pm.WatchInstalls(ctx)
	go pm.Probe(ctx)
	if !*noMDNS {
		err := mdns.Browse(ctx, mdns.TypeNode, func(peerID, fleetID, addr string, port uint16) {
			if fleetID == fleet.GetFleetId() && peerID != id.Fingerprint() {
				pm.Discovered(peerID, net.JoinHostPort(addr, fmt.Sprint(port)))
			}
		})
		if err != nil {
			log.Warn("peer discovery unavailable", "err", err)
		}
	}
	// The Pool the Console's gauges show is the default Space's (decision 123).
	defaultSpace := func(ctx context.Context) string {
		spaces, err := storage.Spaces(ctx, st)
		if err != nil {
			return ""
		}
		if b := storage.DefaultSpace(spaces); b != nil {
			return b.GetSpaceId()
		}
		return ""
	}
	collector := metrics.New(func() string {
		id := defaultSpace(ctx)
		if id == "" {
			return ""
		}
		dir := filepath.Join(*poolsDir, id)
		if !isMountpoint(dir) {
			return ""
		}
		return dir
	})
	// Every mounted Drive this Node owns, for the per-Drive fill gauges.
	collector.Drives = func() map[string]string {
		out := map[string]string{}
		recs, err := store.ListLive(ctx, st, storage.KindDrive, func() *maitrev1.DriveRecord { return &maitrev1.DriveRecord{} })
		if err != nil {
			return out
		}
		for _, d := range recs {
			if d.GetNodeId() != id.Fingerprint() {
				continue
			}
			if dir := filepath.Join(pool.DrivesDir, d.GetDriveId()); isMountpoint(dir) {
				out[d.GetDriveId()] = dir
			}
		}
		return out
	}
	svc.Metrics = collector.Read
	// Decision 210: the long-term metrics, a reading every 10 s.
	if hist, err := history.Open(filepath.Join(*state, "metrics")); err != nil {
		log.Warn("metrics history unavailable", "err", err)
	} else {
		defer hist.Close()
		svc.History = hist
		own := metrics.New(collector.PoolDir)
		own.Drives = collector.Drives
		sampler := &history.Sampler{Node: own.Read, Services: func() []string {
			specs, err := store.ListLive(ctx, st, services.KindService, func() *maitrev1.ServiceSpec { return &maitrev1.ServiceSpec{} })
			if err != nil {
				return nil
			}
			var names []string
			for _, sp := range specs {
				if sp.GetNodeId() == id.Fingerprint() {
					names = append(names, sp.GetName())
				}
			}
			return names
		}}
		go recordHistory(ctx, log, hist, sampler)
	}
	svc.Updates = update.New(nil, log)
	// Milestone 6: record what this Node runs, for the staged rollout, and
	// let the rollout runner act on the Fleet's policy every hour.
	current := func() (string, string) {
		st, err := svc.Updates.Status(ctx)
		if err != nil || st.GetBooted() == nil {
			return "", ""
		}
		return st.GetBooted().GetImage(), st.GetBooted().GetDigest()
	}
	if ref, digest := current(); ref != "" && (self.GetImageDigest() != digest || self.GetImageRef() != ref) {
		self.ImageRef, self.ImageDigest, self.BootedUnixMs = ref, digest, time.Now().UnixMilli()
		if _, err := st.Put(ctx, store.KindNode, id.Fingerprint(), &self, false); err != nil {
			log.Warn("own node record", "err", err)
		}
	}
	// Decision 214: the Node says in its own record that it is updating,
	// before the update starts and until it is back, so peers and the
	// Console show it as updating while it is offline; only it writes that.
	svc.Updates.OnStart = func(image string) {
		_, from := current()
		editSelf(func(n *maitrev1.Node) bool {
			update.Begin(n, image, from, update.BootID(), time.Now())
			return true
		})
	}
	svc.Updates.OnEnd = func(outcome maitrev1.UpdateRun_Outcome, msg string) {
		_, digest := current()
		editSelf(func(n *maitrev1.Node) bool { return update.End(n, outcome, digest, msg, time.Now()) })
	}
	// Back up: close the update this Node was in, if any.
	_, booted := current()
	editSelf(func(n *maitrev1.Node) bool {
		return update.Back(n, update.BootID(), booted, update.RolledBackAt(), time.Now())
	})
	ro := rollout.New(st, svc.Updates, log, id.Fingerprint())
	ro.Current = current
	svc.Rollout = func() string { return ro.Last.Reason }
	svc.Available = ro.Available
	go ro.Run(ctx)
	// Services: Catalog from the image, Quadlet units, Caddy routes, mDNS names.
	cat := serviceservice.NewCatalog(nil, "")
	if raw, err := os.ReadFile(*catalogPath); err == nil {
		if idx, err := catalog.ParseIndex(raw); err == nil {
			cat.Set(idx, "image:"+*catalogPath)
			log.Info("catalog loaded", "entries", len(idx.Entries), "path", *catalogPath)
		} else {
			log.Error("catalog index invalid", "err", err)
		}
	} else {
		log.Warn("no catalog index", "path", *catalogPath)
	}
	// Milestone 5: Remote and Public Services get tsnet nodes of their own;
	// Public through the user's own domain goes through Caddy (decision 112).
	rem := remote.New(st, log, *state)
	defer rem.Close()
	dir := direct.New(log)
	go dir.Run(ctx)
	svc.PublicIP = dir.PublicIP
	// Decision 115: UPnP port forwarding, only for what Services asked for.
	pmap := portmap.New(log, func() string {
		ip, _ := depot.LANAddress()
		return ip
	})
	go pmap.Run(ctx)
	svc.PortMapping = pmap.Router
	exposureStatus := func(name string) (string, string) {
		if u, s := rem.Status(name); u != "" || s != "" {
			return u, s
		}
		return dir.Status(name)
	}
	spaceOwner := func(ctx context.Context, id string) uint32 {
		spaces, _ := storage.Spaces(ctx, st)
		for _, sp := range spaces {
			if sp.GetSpaceId() == id {
				return storage.OwnerOf(sp)
			}
		}
		return 0
	}
	mgr := services.New(services.Config{UnitsDir: *unitsDir, PoolsDir: *poolsDir, DefaultSpace: defaultSpace, SpaceOwner: spaceOwner, Log: log, Remote: exposureStatus, Forward: pmap.State,
		DeviceSigned: policy.SignedByDevice, Authentic: policy.SignedBySelf, ValidateUnits: true,
		LocalPool: func(ctx context.Context, space string) (string, bool) {
			spaces, _ := storage.Spaces(ctx, st)
			for _, b := range spaces {
				if b.GetSpaceId() == space && b.GetSpanNodes() {
					if dir := filepath.Join(pool.SharedDir, space); pool.Mounted(dir) {
						return dir, true
					}
					return "", true
				}
			}
			return "", false
		}}, st, nil)
	ssvc := serviceservice.New(mgr, cat)
	svc.Services = func(ctx context.Context) []*maitrev1.ServiceStatus {
		list, err := mgr.List(ctx)
		if err != nil {
			log.Warn("fleet status: list services", "err", err)
		}
		return list
	}
	// The last metrics this Node gossiped: the election (decision 218) reads
	// these rather than taking a sample of its own.
	var ownMetrics atomic.Pointer[maitrev1.NodeMetrics]
	// ADR 0006: gossip to every peer. Metrics go every 5 s; Service
	// statuses ride along only when one changed (checked every 2 s) or
	// every 5 minutes, so a quiet Fleet sends almost nothing but samples.
	go func() {
		var lastMetrics, lastServices time.Time
		var lastKey string
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			own := svc.OwnStatus(ctx)
			ownMetrics.Store(own.GetMetrics())
			key := statusKey(own.GetServices())
			services := key != lastKey || time.Since(lastServices) >= 5*time.Minute
			metrics := time.Since(lastMetrics) >= 5*time.Second
			if !services && !metrics {
				continue
			}
			if !services {
				own.Services, own.ServicesUnixMs = nil, 0
			}
			pm.Gossip(context.WithoutCancel(ctx), own)
			if services {
				lastServices, lastKey = time.Now(), key
			}
			lastMetrics = time.Now()
		}
	}()
	// Drives (Milestone 4): changes reassemble the Pool in place and sync.
	stor := storage.New(st, nil)
	// The Installer's data partition is the Node's built-in Drive: recorded
	// once, then treated like any other (name, tags, Tier rules).
	if wrote, err := stor.AdoptInternal(ctx); err != nil {
		log.Warn("built-in drive", "err", err)
	} else if wrote {
		log.Info("built-in drive recorded")
		if recs, err := st.List(ctx, storage.KindDrive); err == nil {
			pm.Push(context.WithoutCancel(ctx), recs...)
		}
	}
	// Decision 121: Tiers became Spaces. Old records convert once (the former
	// default Tier becomes the default Space) and files at a Drive's root move
	// into that Space's folder.
	if _, errs := stor.MountAll(ctx); len(errs) > 0 {
		for _, e := range errs {
			log.Warn("drive", "err", e)
		}
	}
	if converted, err := stor.MigrateTiers(ctx); err != nil {
		log.Warn("tier migration", "err", err)
	} else if converted {
		log.Info("tiers converted to spaces")
		if recs, err := st.List(ctx, storage.KindSpace); err == nil {
			pm.Push(context.WithoutCancel(ctx), recs...)
		}
	}
	if def := defaultSpace(ctx); def != "" {
		if err := stor.MoveRootFilesIntoSpace(ctx, def); err != nil {
			log.Warn("drive layout", "err", err)
		}
	}
	var lastMissing string
	var retryAssemble atomic.Bool
	var requestAssemble func()
	assemblePools := func() {
		_, errs := stor.MountAll(ctx)
		for _, e := range errs {
			log.Warn("drive", "err", e)
		}
		bySpace, noCreate, err := stor.LocalBranches(ctx)
		if err != nil {
			log.Warn("spaces", "err", err)
			return
		}
		spaces, _ := storage.Spaces(ctx, st)
		placement := map[string]pool.Placement{}
		spaceByID := map[string]*maitrev1.Space{}
		for _, b := range spaces {
			placement[b.GetSpaceId()] = pool.PlacementOf(b.GetPlacement(), b.GetPreferLocal())
			spaceByID[b.GetSpaceId()] = b
		}
		// Spanning Tiers (105, 106): peers' Pools after our own Drives. The
		// default Tier can span too (user, 2026-10-26: "every Drive, across
		// Nodes" is the default Pool spanning, not a second Tier that
		// empties it).
		run := storage.ExecRunner{}
		addrs := pm.Addresses()
		best := map[string]string{}
		for id, as := range addrs {
			if len(as) > 0 {
				best[id] = as[0]
			}
		}
		if err := storage.WriteNFSPeers(ctx, st, best); err != nil {
			log.Warn("nfs peers", "err", err)
		}
		remote, missing, err := stor.RemoteBranches(ctx, run, best)
		if err != nil {
			log.Warn("remote pools", "err", err)
		}
		// A peer that is off, still booting or not exporting yet is mounted
		// on a later try; nothing else would retry until a record changed.
		if why := strings.Join(missing, "; "); why != lastMissing {
			lastMissing = why
			if why != "" {
				log.Warn("remote pools not mounted, retrying each minute", "missing", why)
			}
		}
		if len(missing) > 0 && retryAssemble.CompareAndSwap(false, true) {
			time.AfterFunc(time.Minute, func() {
				retryAssemble.Store(false)
				requestAssemble()
			})
		}
		// Decision 258: a Spanning Space with a Parity Level puts new files
		// only on Nodes with enough Parity Drives for it, when any has.
		parityCount := storage.ParityDriveCounts(ctx, st)
		var spaceIDs []string
		for space, branches := range bySpace {
			spaceIDs = append(spaceIDs, space)
			storage.MarkSpaceFolders(branches)
			all := append(append([]string(nil), branches...), remote[space]...)
			nc := noCreate
			if b := spaceByID[space]; b.GetSpanNodes() && b.GetParityLevel() > 0 && !b.GetMirrored() {
				nc = storage.UnprotectedNoCreate(noCreate, branches, remote[space], id.Fingerprint(), int(b.GetParityLevel()), parityCount)
			}
			if err := pool.Assemble(ctx, space, all, len(branches), placement[space], nc); err != nil {
				log.Warn("pool", "space", space, "err", err)
			}
			// What peers mount: our own branches only (decision 188).
			if err := pool.AssembleShared(ctx, space, branches, placement[space], noCreate); err != nil {
				log.Warn("shared pool", "space", space, "err", err)
			}
		}
		peerSet := map[string]bool{}
		for id := range addrs {
			peerSet[id] = true
		}
		storage.UnmountStale(ctx, run, peerSet)
		owners := map[string]uint32{}
		for _, id := range spaceIDs {
			owners[id] = spaceOwner(ctx, id)
		}
		if err := storage.WriteExports(ctx, run, spaceIDs, owners, storage.PeerHosts(pm.Status(ctx), addrs)); err != nil {
			log.Warn("nfs exports", "err", err)
		}
	}
	// Assembly mounts peers' Pools over NFS (up to 20 s each) and must not
	// run inside a PutRecord: a Console creating a spanning Tier timed out
	// waiting. Requests coalesce and run one at a time in the background.
	assembleNow := make(chan struct{}, 1)
	requestAssemble = func() {
		select {
		case assembleNow <- struct{}{}:
		default:
		}
	}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-assembleNow:
			}
			time.Sleep(time.Second)
			assemblePools()
		}
	}()
	// Placements that order the Drives by free space (decisions 201, 202)
	// re-sort as they fill.
	go func() {
		t := time.NewTicker(10 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				requestAssemble()
			}
		}
	}()
	stor.OnChange = func() {
		requestAssemble()
		if recs, err := st.List(ctx, storage.KindDrive); err == nil {
			pm.Push(context.WithoutCancel(ctx), recs...)
		}
		requestBackup()
	}
	// Space or Drive records arriving from a Console or a peer reshape the Pools.
	withSpaces := svc.OnChange
	svc.OnChange = func(r *maitrev1.Record) {
		withSpaces(r)
		// A Node record may bring a peer's NFS certificate (decision 188).
		if r.GetKind() == storage.KindSpace || r.GetKind() == store.KindNode || (r.GetKind() == storage.KindDrive && r.GetOwnerNodeId() == id.Fingerprint()) {
			requestAssemble()
		}
	}
	withPools := pm.OnApplied
	pm.OnApplied = func() {
		withPools()
		requestAssemble()
	}
	assemblePools()
	ssvc2 := storageservice.New(stor)
	// ADR 0009: Drain empties a Drive (a Parity Drive to be, a rule change
	// that took it out of a Space) without its files vanishing from Pools.
	dm := drain.New(st, log, id.Fingerprint())
	dm.StateFile = filepath.Join(*state, "drains.json")
	dm.Services = mgr.Drainer()
	dm.OnChange = stor.OnChange
	ssvc2.Drains = dm
	go dm.Run(ctx)
	// SnapRAID parity per Space on this Node's Parity Drives, synced on
	// the schedule; status travels with the gossip.
	pmgr := parity.New(st, log, id.Fingerprint(), stor, dm)
	pmgr.StateFile = filepath.Join(*state, "parity.json")
	ssvc2.Parity = pmgr
	go pmgr.Schedule(ctx)
	svc.Parity = pmgr.Status
	svc.Drains = dm.Jobs
	// Copies per Space: changed files copied as they go quiet, a full pass
	// each hour; Mirrored Drives checked for the gossip.
	cm := copies.New(st, log, id.Fingerprint(), stor)
	ssvc2.Copies = cm
	go cm.Run(ctx)
	go cm.Watch(ctx)
	svc.Copies = cm.Statuses
	svc.Mirrors = cachedMirrors(stor.MirrorStatuses, 5*time.Minute)
	// A Space's Parity Level or a Drive change reshapes the arrays.
	withParity := svc.OnChange
	svc.OnChange = func(r *maitrev1.Record) {
		withParity(r)
		if r.GetKind() == storage.KindSpace || r.GetKind() == storage.KindDrive {
			go func() {
				if _, err := pmgr.WriteConfigs(ctx); err != nil {
					log.Warn("parity configs", "err", err)
				}
			}()
		}
	}
	// A Space or Drive record that changed may have taken a Drive out of a
	// Space: its files there move to the Space's other Drives (247).
	requestSweep := func() {
		go func() {
			time.Sleep(2 * time.Second)
			dm.Sweep(ctx)
		}()
	}
	withDrain := svc.OnChange
	svc.OnChange = func(r *maitrev1.Record) {
		withDrain(r)
		if r.GetKind() == storage.KindSpace || (r.GetKind() == storage.KindDrive && r.GetOwnerNodeId() == id.Fingerprint()) {
			requestSweep()
		}
	}
	requestSweep()
	var hostAd *mdns.Advertisement
	// Decision 218: the roles this Node holds (the router forwards the app's
	// port, or the web's, here), what the domain's DNS says, and the
	// election's last outcome, so a change of holder republishes.
	var entryMu sync.Mutex
	held := map[string]bool{}
	dnsState := map[string]string{}
	lastElected := ""
	me := id.Fingerprint()
	elect := func(ctx context.Context) (*maitrev1.FleetDomain, string, string) {
		fd := &maitrev1.FleetDomain{}
		_, _ = st.Get(ctx, entry.Kind, entry.ID, fd)
		entryMu.Lock()
		var mine []string
		for r, ok := range held {
			if ok {
				mine = append(mine, r)
			}
		}
		entryMu.Unlock()
		cands := []entry.Candidate{{ID: me, Reachable: true, Busy: entry.Busyness(ownMetrics.Load()), Holds: mine}}
		for _, p := range pm.Status(ctx) {
			c := entry.Candidate{ID: p.GetNodeId(), Reachable: p.GetOnline()}
			if last := pm.Last(p.GetNodeId()); last != nil {
				c.Busy = entry.Busyness(last.GetMetrics())
				for _, h := range last.GetEntries() {
					c.Holds = append(c.Holds, h.GetRole())
				}
			}
			cands = append(cands, c)
		}
		return fd, entry.Elect(fd.GetApp(), entry.App, cands), entry.Elect(fd.GetWeb(), entry.Web, cands)
	}
	svc.Entries = func() []*maitrev1.EntryHold {
		entryMu.Lock()
		defer entryMu.Unlock()
		var out []*maitrev1.EntryHold
		for _, r := range []struct{ role, owner string }{{entry.App, entry.App}, {entry.Web, direct.ForwardOwner}} {
			if !held[r.role] {
				continue
			}
			state := pmap.State(r.owner)
			if d := dnsState[r.role]; d != "" {
				state += "; " + d
			}
			out = append(out, &maitrev1.EntryHold{Role: r.role, State: state})
		}
		return out
	}
	// The IP addresses known for each peer, best first: Caddy is given
	// addresses, not names (it may not resolve .local).
	peerIPs := func() map[string][]string {
		out := map[string][]string{}
		for id, addrs := range pm.Addresses() {
			for _, a := range addrs {
				if host, _, err := net.SplitHostPort(a); err == nil && net.ParseIP(host) != nil {
					out[id] = append(out[id], host)
				}
			}
		}
		return out
	}
	// Where each peer's Caddy answers: its best IP address, port 80.
	peerWeb := func(ctx context.Context) map[string]string {
		out := map[string]string{}
		for id, ips := range peerIPs() {
			out[id] = net.JoinHostPort(ips[0], "80")
		}
		return out
	}
	publish := func() {
		specs, err := store.ListLive(ctx, st, services.KindService, func() *maitrev1.ServiceSpec { return &maitrev1.ServiceSpec{} })
		if err != nil {
			log.Error("list services", "err", err)
			return
		}
		var routes, public, plain, here []caddy.Route
		var names []string
		var wants []portmap.Mapping
		fd, appHolder, webHolder := elect(ctx)
		holdApp, holdWeb, webOn := appHolder == me, webHolder == me, fd.GetWeb().GetEnabled()
		entryMu.Lock()
		held[entry.App], held[entry.Web] = holdApp, holdWeb
		lastElected = appHolder + "|" + webHolder
		entryMu.Unlock()
		peersWeb := peerWeb(ctx)
		// The Fleet database holds every Node's Services. Caddy here routes
		// all of them (decision 218): its own to their loopback ports, a
		// peer's to that peer's Caddy as a hop, so a request that reached
		// the wrong Node still arrives. Only its own are announced on mDNS
		// (two Nodes would claim a name) and checked and forwarded here.
		var mine []*maitrev1.ServiceSpec
		ownPublic := false
		for _, sp := range specs {
			local := sp.GetNodeId() == me
			if local {
				mine = append(mine, sp)
			}
			if sp.GetWebHostPort() == 0 {
				continue
			}
			r := caddy.Route{Upstream: fmt.Sprintf("127.0.0.1:%d", sp.GetWebHostPort())}
			if local {
				here = append(here, caddy.Route{Host: sp.GetName(), Upstream: r.Upstream})
			} else if addr := peersWeb[sp.GetNodeId()]; addr != "" {
				r.Upstream, r.Hop = addr, sp.GetName()
			} else {
				continue
			}
			r.Host = sp.GetName() + "." + services.DefaultSuffix
			routes = append(routes, r)
			if sp.GetExposure() != direct.Exposure || !direct.ValidHost(sp.GetPublicHost()) {
				continue
			}
			r.Host = sp.GetPublicHost()
			// With a web entry Node, it alone has certificates for every
			// public host and the router sends it 80 and 443; before one,
			// each Node publishes its own as it did.
			switch {
			case webOn && holdWeb, !webOn && local:
				public = append(public, r)
				ownPublic = ownPublic || local
			default:
				plain = append(plain, r)
			}
		}
		specs = mine
		for _, sp := range specs {
			if sp.GetForwardPorts() && !sp.GetInfrastructure() {
				for _, hp := range sp.GetHostPorts() {
					if mp, ok := portmap.Parse(hp); ok {
						mp.Owner = sp.GetName()
						wants = append(wants, mp)
					}
				}
			}
			if sp.GetWebHostPort() != 0 {
				names = append(names, sp.GetName()+"."+services.DefaultSuffix)
			}
		}
		if holdWeb || (!webOn && ownPublic) {
			// Caddy must be reachable from the internet for the ACME
			// challenge and the Services themselves.
			wants = append(wants, portmap.Mapping{Port: 80, Proto: "tcp", Owner: direct.ForwardOwner}, portmap.Mapping{Port: 443, Proto: "tcp", Owner: direct.ForwardOwner})
		}
		if holdApp {
			wants = append(wants, portmap.Mapping{Port: entry.AppPort, Proto: "tcp", Owner: entry.App})
		}
		pmap.Reconcile(wants)
		if *caddyAdmin != "" {
			var ps maitrev1.PublicSettings
			_, _ = st.Get(ctx, "public_settings", "fleet", &ps)
			var nodes []string
			for _, ips := range peerIPs() {
				nodes = append(nodes, ips...)
			}
			if err := caddy.Load(ctx, *caddyAdmin, caddy.Build(caddy.Site{ACMEEmail: ps.GetAcmeEmail(), Routes: routes, Public: public, Plain: plain, Here: here, Peers: nodes})); err != nil {
				log.Warn("caddy config not applied", "err", err)
			}
		}
		// This Node's public hosts are checked where their certificates
		// are: on the web entry Node when there is one.
		tlsAddr := "127.0.0.1:443"
		if webOn && webHolder != "" && !holdWeb {
			if host, _, err := net.SplitHostPort(peersWeb[webHolder]); err == nil {
				tlsAddr = net.JoinHostPort(host, "443")
			}
		}
		dir.SetTLSAddr(tlsAddr)
		dir.Reconcile(specs)
		if !*noTailscale {
			rem.Reconcile(ctx, specs)
		}
		if !*noMDNS {
			if hostAd != nil {
				hostAd.Close()
			}
			var v4 []string
			for _, a := range installer.Addresses() {
				if !strings.Contains(a, ":") {
					v4 = append(v4, a)
				}
			}
			ad, err := mdns.PublishHosts(names, v4)
			if err != nil {
				log.Warn("mDNS names not published", "err", err)
			} else {
				hostAd = ad
			}
		}
	}
	ssvc.OnChange = publish
	mgr.OnExposure = publish
	publish()
	// Decision 114: the Fleet-wide public settings (ACME email) reach Caddy
	// whether a Console wrote them here or a peer synced them.
	withPublic := svc.OnChange
	svc.OnChange = func(r *maitrev1.Record) {
		withPublic(r)
		if r.GetKind() == "public_settings" || r.GetKind() == entry.Kind {
			publish()
		}
	}
	// Decision 218: the election follows the Fleet's load and reachability,
	// re-run every 15 s; a new holder anywhere republishes here. The Node
	// holding a role checks the domain's DNS every minute.
	go func() {
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		var lastDNS time.Time
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			fd, app, web := elect(ctx)
			entryMu.Lock()
			changed := app+"|"+web != lastElected
			holding := held[entry.App] || held[entry.Web]
			entryMu.Unlock()
			if changed {
				log.Info("entry nodes", "app", app, "web", web)
				publish()
			}
			if holding && fd.GetDomain() != "" && time.Since(lastDNS) >= time.Minute {
				lastDNS = time.Now()
				next := map[string]string{}
				entryMu.Lock()
				roles := map[string]bool{entry.App: held[entry.App], entry.Web: held[entry.Web]}
				entryMu.Unlock()
				for r, ok := range roles {
					if ok {
						next[r] = entry.CheckDNS(ctx, net.DefaultResolver.LookupHost, fd.GetDomain(), dir.PublicIP(), r == entry.Web)
					}
				}
				entryMu.Lock()
				dnsState = next
				entryMu.Unlock()
			}
		}
	}()
	withPublicApplied := pm.OnApplied
	pm.OnApplied = func() {
		withPublicApplied()
		publish()
	}
	// Decision 189: a Service with protected options runs only while each
	// has a grant; a revoked grant stops it.
	mgr.EnforceGrants(ctx)
	withGrants := svc.OnChange
	svc.OnChange = func(r *maitrev1.Record) {
		withGrants(r)
		if r.GetKind() == services.KindGrant {
			mgr.EnforceGrants(ctx)
		}
	}
	withGrantsApplied := pm.OnApplied
	pm.OnApplied = func() {
		withGrantsApplied()
		mgr.EnforceGrants(ctx)
	}
	// Milestone 6: Services that opted in check for a newer image daily,
	// an hour after boot and every 24 hours after that.
	go func() {
		t := time.NewTimer(time.Hour)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			mgr.AutoUpdate(ctx)
			publish()
			t.Reset(24 * time.Hour)
		}
	}()
	defer func() {
		if hostAd != nil {
			hostAd.Close()
		}
	}()

	// Recovery-only callers (the Recovery Phrase, decision 100) reach one
	// procedure: PutRecord, which itself limits them to adding a device.
	rpc := connect.NewServer(func(next connect.ServerFunc) connect.ServerFunc {
		return func(ctx context.Context, spec connect.Spec, stream connect.ServerStream) error {
			if p, ok := peer.From(ctx); ok && list.Limited(p.Fingerprint) && spec.Procedure != "/maitre.v1.NodeService/PutRecord" {
				return connect.NewError(connect.CodePermissionDenied, "the Recovery Phrase may only add a Trusted Device")
			}
			return next(ctx, spec, stream)
		}
	})
	maitrev1connect.RegisterNodeServiceHandler(rpc, svc)
	maitrev1connect.RegisterServiceServiceHandler(rpc, ssvc)
	maitrev1connect.RegisterStorageServiceHandler(rpc, ssvc2)
	// Decision 122: the file manager's back end.
	fm := files.New(st, log, id.Fingerprint())
	mgr.Mover = fm
	fm.PeerAddr = func(nodeID string) string {
		if as := pm.Addresses()[nodeID]; len(as) > 0 {
			return as[0]
		}
		return ""
	}
	fm.PeerHTTP = pm.HTTPClient
	go fm.Run(ctx)
	fsvc := filesservice.New(fm)
	fsvc.IsNode = list.Node
	maitrev1connect.RegisterFilesServiceHandler(rpc, fsvc)
	mux := http.NewServeMux()
	connecthttp.Mount(mux, rpc)
	// Decision 183: file contents for the file manager, same callers as RPCs.
	mux.Handle(files.ContentPath, fm.Handler(func(r *http.Request) bool {
		p, ok := peer.From(r.Context())
		return ok && !list.Limited(p.Fingerprint)
	}))

	// Milestone 5: join the Fleet's tailnet as this Node, so the Console
	// reaches it from anywhere; the mTLS listener is served there as well.
	var ts *tailnet.Node
	startTailscale := func() {
		if *noTailscale || ts != nil {
			return
		}
		var cfg maitrev1.TailscaleConfig
		if _, err := st.Get(ctx, tailnet.KindTailscale, "fleet", &cfg); err != nil {
			return
		}
		dir := tailnet.StateDir(*state, "node")
		key := ""
		if _, err := os.Stat(filepath.Join(dir, "tailscaled.state")); err != nil {
			// First join: an auth key from the OAuth client; afterwards tsnet
			// has its own node key and needs none.
			k, err := tailnet.AuthKey(ctx, &cfg, false)
			if err != nil {
				log.Warn("tailscale", "err", err)
			} else {
				key = k
			}
		}
		ts = tailnet.Start(ctx, log, dir, hostLabel(self.GetName()), key)
		svc.Tailscale = ts.Status
		go func() {
			// Serve the agent on the tailnet once the node is up, and
			// advertise the tailnet address in our Node record.
			for ctx.Err() == nil {
				stt := ts.Status()
				if stt.GetState() == "Running" && len(stt.GetAddresses()) > 0 {
					break
				}
				time.Sleep(2 * time.Second)
			}
			if ctx.Err() != nil {
				return
			}
			ln, err := ts.Listen(int(listenPort(*listen)))
			if err != nil {
				log.Warn("tailscale listener", "err", err)
				return
			}
			stt := ts.Status()
			addr := net.JoinHostPort(stt.GetAddresses()[0], fmt.Sprint(listenPort(*listen)))
			editSelf(func(n *maitrev1.Node) bool {
				if slices.Contains(n.GetAddresses(), addr) {
					return false
				}
				n.Addresses = append(n.Addresses, addr)
				return true
			})
			log.Info("tailscale: reachable", "dns", stt.GetDnsName(), "addr", addr)
			srv := &http.Server{Handler: peer.Middleware(gate.Handler(list, svcproxy.Header, svcproxy.Handler(st, mux))), TLSConfig: mtls.ServerConfig(id, list), ReadHeaderTimeout: 10 * time.Second}
			go func() { <-ctx.Done(); _ = srv.Close() }()
			if err := srv.ServeTLS(ln, "", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Warn("tailscale serve", "err", err)
			}
		}()
	}
	startTailscale()
	withTailscale := svc.OnChange
	svc.OnChange = func(r *maitrev1.Record) {
		withTailscale(r)
		if r.GetKind() == tailnet.KindTailscale {
			startTailscale()
		}
	}
	withTailscaleApplied := pm.OnApplied
	pm.OnApplied = func() {
		withTailscaleApplied()
		startTailscale()
	}

	mdnsState := "off"
	started := time.Now()
	// Decision 101: a pending recovery code opens the recovery listener for
	// 15 minutes and is announced so a new Console can find this Node.
	recovering := recoverymode.Pending(*state)
	var nodeAd *mdns.Advertisement
	announce := func() {
		if *noMDNS {
			return
		}
		if nodeAd != nil {
			nodeAd.Close()
			nodeAd = nil
		}
		txt := map[string]string{"id": id.Fingerprint(), "fleet": fleet.GetFleetId(), "name": self.GetName(), "v": version}
		if recovering {
			txt["recovery"] = "1"
		}
		ad, err := mdns.Publish(mdns.InstanceName(self.GetName(), id.Fingerprint()), mdns.TypeNode, listenPort(*listen), txt)
		if err != nil {
			log.Warn("mDNS advertisement unavailable", "err", err)
			mdnsState = "not announced: " + err.Error()
		} else {
			nodeAd = ad
			mdnsState = "announced as " + self.GetName()
		}
	}
	announce()
	defer func() {
		if nodeAd != nil {
			nodeAd.Close()
		}
	}()
	// A rename from the Console: record, host name and announcement follow
	// at once; the tailnet name follows on the next start.
	svc.Rename = func(ctx context.Context, name string) (*maitrev1.Node, error) {
		var cur maitrev1.Node
		if _, err := st.Get(ctx, store.KindNode, id.Fingerprint(), &cur); err != nil {
			return nil, err
		}
		cur.Name = name
		if _, err := st.Put(ctx, store.KindNode, id.Fingerprint(), &cur, false); err != nil {
			return nil, err
		}
		self.Name = name
		if rec, err := st.Get(ctx, store.KindNode, id.Fingerprint(), &cur); err == nil {
			pm.Push(context.WithoutCancel(ctx), rec)
		}
		ensureHostname(log, hostLabel(name))
		announce()
		log.Info("node renamed", "name", name)
		return &cur, nil
	}
	if recovering {
		rsvc := recoverymode.NewService(*state, st, id.Fingerprint(), fleet)
		rctx, rcancel := context.WithTimeout(ctx, recoverymode.Window)
		rsvc.OnAdded = func(rec *maitrev1.Record) {
			refreshTrust()
			pm.Push(context.WithoutCancel(ctx), rec)
			log.Info("recovery: device added", "device", rec.GetId())
			rcancel()
		}
		rrpc := connect.NewServer()
		maitrev1connect.RegisterRecoveryServiceHandler(rrpc, rsvc)
		rmux := http.NewServeMux()
		connecthttp.Mount(rmux, rrpc)
		host, _, _ := net.SplitHostPort(*listen)
		go func() {
			defer rcancel()
			log.Info("recovery window open", "minutes", int(recoverymode.Window.Minutes()), "port", recoverymode.Port)
			if err := serve(rctx, log, net.JoinHostPort(host, fmt.Sprint(*recoveryPort)), peer.Middleware(rmux), mtls.InstallerServerConfig(id)); err != nil {
				log.Warn("recovery listener", "err", err)
			}
			recoverymode.Discard(*state)
			log.Info("recovery window closed")
		}()
	}
	// The machine's console (decision 72 revised): a text interface on tty1
	// when the keyboard is ours, a plain live status page otherwise.
	if !*noScreen {
		src := &agentSource{status: func() tui.Status {
			names := []string{}
			if specs, err := store.ListLive(ctx, st, services.KindService, func() *maitrev1.ServiceSpec { return &maitrev1.ServiceSpec{} }); err == nil {
				for _, sp := range specs {
					// This Node's own: the Logs page reads their journals.
					if sp.GetNodeId() == id.Fingerprint() {
						names = append(names, sp.GetName())
					}
				}
			}
			return tui.Status{Version: version, Node: self.GetName(), Fleet: fleet.GetName(), Addresses: installer.Addresses(), Discovery: mdnsState, Trusted: list.Len(), Services: names, Started: started}
		}, metrics: svc.Metrics, history: svc.History, stateDir: *state}
		if term.IsTerminal(int(os.Stdin.Fd())) {
			go func() {
				if err := tui.Run(ctx, src); err != nil {
					log.Warn("screen stopped", "err", err)
				}
			}()
		} else {
			go plainStatusPage(ctx, src)
		}
	}
	return serve(ctx, log, *listen, peer.Middleware(gate.Handler(list, svcproxy.Header, svcproxy.Handler(st, mux))), mtls.ServerConfig(id, list))
}

// runInstaller is `maitre installer`: the mode booted from installer media.
func runInstaller(args []string) error {
	fs := flag.NewFlagSet("installer", flag.ExitOnError)
	state := fs.String("state", "/run/maitre-installer", "scratch directory for the Installer's identity")
	listen := fs.String("listen", fmt.Sprintf("0.0.0.0:%d", port), "address to listen on")
	image := fs.String("image", kernelArg("maitre.image"), "container image to install (default from maitre.image= kernel argument)")
	insecure := fs.String("insecure-registry", kernelArg("maitre.insecure_registry"), "testing: registry host:port to pull from without TLS (default from maitre.insecure_registry=)")
	noMDNS := fs.Bool("no-mdns", false, "do not advertise on mDNS")
	noScreen := fs.Bool("no-screen", false, "do not redraw the status screen on stdout")
	noReboot := fs.Bool("no-reboot", false, "do not reboot after a successful install")
	fake := fs.String("fake-install", "", "development: skip bootc and seed the config partition into this directory")
	direct := fs.Bool("direct", hasKernelArg("maitre.netboot"), "run bootc on this system with --source-imgref instead of from a podman container (default when booted with maitre.netboot)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if *image == "" {
		return errors.New("no image to install: pass --image or boot with maitre.image=<ref>")
	}
	if *insecure != "" && *fake == "" {
		if err := installer.AllowInsecureRegistry(*insecure); err != nil {
			return err
		}
		log.Warn("pulling from an insecure registry (testing only)", "registry", *insecure)
	}

	id, err := identity.LoadOrGenerate(filepath.Join(*state, "identity"))
	if err != nil {
		return err
	}
	opts := installer.Options{Version: version, ImageRef: *image, InsecureRegistry: *insecure}
	sourceNote := "downloading from " + *image
	if *fake == "" {
		// Decision 76: media may carry the image; then bootc installs from it
		// directly, as the netboot path already does.
		local, err := installer.FindLocalImage(context.Background())
		if err != nil {
			log.Warn("media image unusable", "err", err)
		}
		if local != nil {
			src, why := installer.ChooseSource(context.Background(), *image, local)
			log.Info("install source", "source", src, "why", why)
			opts.Source, sourceNote = src, why
			*direct = true
		}
	}
	if *direct && *fake == "" {
		opts.Runner = &installer.BootcRunner{Direct: true}
		log.Info("installing with bootc --source-imgref", "image", *image, "source", opts.Source)
	}
	if *fake != "" {
		opts.Runner = &installer.FakeRunner{Dir: *fake, Delay: 2 * time.Second}
		opts.Disks = installer.FakeDisks
		opts.Folders = installer.FakeFolders{}
	}
	if !*noReboot {
		opts.Reboot = func() error { return exec.Command("systemctl", "reboot").Run() }
	}
	if *fake == "" {
		opts.Logs = func(ctx context.Context, n int) ([]string, error) {
			return tui.JournalLogs(ctx, "maitre-installer.service", n)
		}
	} else {
		opts.Logs = func(context.Context, int) ([]string, error) {
			return []string{"level=INFO msg=\"maitre installer\" fake=true", "level=INFO msg=listening addr=0.0.0.0:7443"}, nil
		}
	}
	in := installer.New(id, opts)
	if *fake == "" {
		ensureHostname(log, "maitre-installer-"+id.Fingerprint()[:8])
	}

	rpc := connect.NewServer()
	maitrev1connect.RegisterInstallerServiceHandler(rpc, in)
	mux := http.NewServeMux()
	connecthttp.Mount(mux, rpc)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if !*noMDNS {
		ad, err := mdns.Publish(mdns.InstanceName("Maître Installer", id.Fingerprint()), mdns.TypeInstaller, listenPort(*listen), map[string]string{
			"id": id.Fingerprint(), "v": version, "machine": installer.MachineName(),
		})
		if err != nil {
			log.Warn("mDNS advertisement unavailable", "err", err)
		} else {
			defer ad.Close()
		}
	}
	if !*noScreen && term.IsTerminal(int(os.Stdin.Fd())) {
		// Decision 261: the Node's look, with the disks to look inside.
		go func() {
			if err := tui.RunInstaller(ctx, &installerSource{in: in, id: id, sourceNote: sourceNote}); err != nil {
				log.Warn("screen stopped", "err", err)
			}
		}()
	} else if !*noScreen {
		go func() {
			t := time.NewTicker(time.Second)
			defer t.Stop()
			for {
				// Clear and redraw: this is a status screen, not a log.
				fmt.Print("\033[H\033[2J" + in.Screen())
				select {
				case <-ctx.Done():
					return
				case <-t.C:
				}
			}
		}()
	}
	log.Info("maitre installer", "installer_id", id.Fingerprint(), "image", *image)
	return serve(ctx, log, *listen, peer.Middleware(mux), mtls.InstallerServerConfig(id))
}

// serve runs an HTTPS server until ctx is done.
func serve(ctx context.Context, log *slog.Logger, addr string, h http.Handler, tlsCfg *tls.Config) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           h,
		TLSConfig:         tlsCfg,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	// Bind before announcing, so "listening" in the log means it.
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	log.Info("listening", "addr", addr)
	// Certificates come from TLSConfig, so the file arguments are empty.
	if err := srv.ServeTLS(ln, "", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// isMountpoint reports whether dir is a mount (the Pool) rather than a plain
// directory on the OS partition.
func isMountpoint(dir string) bool {
	b, err := os.ReadFile("/proc/self/mounts")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
		if f := strings.Fields(line); len(f) > 1 && f[1] == dir {
			return true
		}
	}
	return false
}

// listenPort extracts the port from a host:port listen address.
func listenPort(addr string) uint16 {
	_, p, err := net.SplitHostPort(addr)
	if err != nil {
		return port
	}
	n, err := strconv.Atoi(p)
	if err != nil || n <= 0 || n > 65535 {
		return port
	}
	return uint16(n)
}

// hasKernelArg reports whether a bare flag (or key=anything) is on /proc/cmdline.
func hasKernelArg(key string) bool {
	b, err := os.ReadFile("/proc/cmdline")
	if err != nil {
		return false
	}
	for _, f := range strings.Fields(string(b)) {
		if f == key || strings.HasPrefix(f, key+"=") {
			return true
		}
	}
	return false
}

// kernelArg reads key=<value> from /proc/cmdline.
func kernelArg(key string) string {
	b, err := os.ReadFile("/proc/cmdline")
	if err != nil {
		return ""
	}
	for _, f := range strings.Fields(string(b)) {
		if v, ok := strings.CutPrefix(f, key+"="); ok {
			return v
		}
	}
	return ""
}

// statusKey summarises Service statuses so a change is noticed between gossips.
func statusKey(list []*maitrev1.ServiceStatus) string {
	var b strings.Builder
	for _, st := range list {
		fmt.Fprintf(&b, "%s:%d:%t:%d:%s:%s;", st.GetSpec().GetName(), st.GetState(), st.GetHealthy(), st.GetRestarts(), st.GetMessage(), st.GetForwardState())
	}
	return b.String()
}

// recordHistory takes the readings every history.Every, writes what is in
// memory every minute, and drops series no longer recorded once a day.
func recordHistory(ctx context.Context, log *slog.Logger, hist *history.Store, sampler *history.Sampler) {
	hist.Sweep()
	tick := time.NewTicker(history.Every)
	defer tick.Stop()
	var flushed, swept time.Time
	sampler.Read(time.Now()) // prime the rates
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			if err := hist.Record(now, sampler.Read(now)); err != nil {
				log.Warn("metrics history", "err", err)
			}
			if now.Sub(flushed) >= time.Minute {
				if err := hist.Flush(); err != nil {
					log.Warn("metrics history flush", "err", err)
				}
				flushed = now
			}
			if now.Sub(swept) >= 24*time.Hour {
				if !swept.IsZero() {
					hist.Sweep()
				}
				swept = now
			}
		}
	}
}

// cachedMirrors asks btrfs about the Mirrored Drives at most every so
// often; the gossip asks every five seconds.
func cachedMirrors(check func(context.Context) []*maitrev1.MirrorStatus, every time.Duration) func(context.Context) []*maitrev1.MirrorStatus {
	var mu sync.Mutex
	var last []*maitrev1.MirrorStatus
	var at time.Time
	return func(ctx context.Context) []*maitrev1.MirrorStatus {
		mu.Lock()
		defer mu.Unlock()
		if time.Since(at) >= every {
			last, at = check(ctx), time.Now()
		}
		return last
	}
}
