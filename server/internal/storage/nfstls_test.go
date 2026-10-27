package storage

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/identity"
	"github.com/Onfire7/maitre-de-serveur/server/internal/store"
)

func nfsNode(t *testing.T) (*identity.Identity, string) {
	t.Helper()
	id, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	c, err := id.NFSCertificatePEM()
	if err != nil {
		t.Fatal(err)
	}
	return id, string(c)
}

// The trust store holds each peer's certificate only when it carries that
// peer's key, and /etc/hosts gains a block naming peers without losing what
// was there.
func TestWriteNFSPeers(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	TLSDir = filepath.Join(dir, "pki")
	HostsFile = filepath.Join(dir, "hosts")
	t.Cleanup(func() { TLSDir, HostsFile = "/etc/pki/tlshd", "/etc/hosts" })
	_ = os.WriteFile(HostsFile, []byte("127.0.0.1 localhost\n"), 0o644)

	self, selfCert := nfsNode(t)
	peer, peerCert := nfsNode(t)
	liar, _ := nfsNode(t)
	old, _ := nfsNode(t)
	st, err := store.Open(filepath.Join(dir, "fleet.db"), self.Fingerprint())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	put := func(id *identity.Identity, cert string) {
		if _, err := st.Put(ctx, store.KindNode, id.Fingerprint(), &maitrev1.Node{NodeId: id.Fingerprint(), NfsCertificatePem: cert}, false); err != nil {
			t.Fatal(err)
		}
	}
	put(self, selfCert)
	put(peer, peerCert)
	put(liar, peerCert) // another Node's certificate under its own id
	put(old, "")        // from before NFS over TLS

	addrs := map[string]string{peer.Fingerprint(): "192.168.4.74:7443", old.Fingerprint(): "192.168.4.80:7443"}
	if err := WriteNFSPeers(ctx, st, addrs); err != nil {
		t.Fatal(err)
	}
	trust, _ := os.ReadFile(filepath.Join(TLSDir, "fleet.pem"))
	if string(trust) != peerCert {
		t.Fatalf("trust store should hold only the peer's own certificate:\n%s", trust)
	}
	hosts, _ := os.ReadFile(HostsFile)
	want := "127.0.0.1 localhost\n" + hostsBegin + "\n" +
		"192.168.4.74 " + identity.NFSName(peer.Fingerprint()) + "\n" +
		"192.168.4.80 " + identity.NFSName(old.Fingerprint()) + "\n" + hostsEnd + "\n"
	if peer.Fingerprint() > old.Fingerprint() {
		want = "127.0.0.1 localhost\n" + hostsBegin + "\n" +
			"192.168.4.80 " + identity.NFSName(old.Fingerprint()) + "\n" +
			"192.168.4.74 " + identity.NFSName(peer.Fingerprint()) + "\n" + hostsEnd + "\n"
	}
	if string(hosts) != want {
		t.Fatalf("hosts:\n%s\nwant:\n%s", hosts, want)
	}

	// The peer moved; the block is replaced, not appended to.
	addrs = map[string]string{peer.Fingerprint(): "192.168.4.99:7443"}
	if err := WriteNFSPeers(ctx, st, addrs); err != nil {
		t.Fatal(err)
	}
	hosts, _ = os.ReadFile(HostsFile)
	if want := "127.0.0.1 localhost\n" + hostsBegin + "\n192.168.4.99 " + identity.NFSName(peer.Fingerprint()) + "\n" + hostsEnd + "\n"; string(hosts) != want {
		t.Fatalf("hosts after move:\n%s", hosts)
	}

	// No peers: the block goes away.
	if err := WriteNFSPeers(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	if hosts, _ = os.ReadFile(HostsFile); string(hosts) != "127.0.0.1 localhost\n" {
		t.Fatalf("hosts with no peers:\n%s", hosts)
	}

	if !HasNFSCertificate(ctx, st, peer.Fingerprint()) || HasNFSCertificate(ctx, st, liar.Fingerprint()) || HasNFSCertificate(ctx, st, old.Fingerprint()) {
		t.Fatal("HasNFSCertificate")
	}
}

// Mounts use the peer's NFS name and mutual TLS; exports demand it.
func TestNFSOverTLSOptions(t *testing.T) {
	RemoteDir = t.TempDir()
	t.Cleanup(func() { RemoteDir = "/var/mnt/remote" })
	run := &fakeRunner{}
	mountedDirs = map[string]bool{}
	t.Cleanup(func() { mountedDirs = nil })
	_, _ = MountRemotePool(context.Background(), run, strings.Repeat("ab", 32), "media")
	if len(run.cmds) != 1 || !strings.Contains(run.cmds[0], "xprtsec=mtls") || !strings.Contains(run.cmds[0], identity.NFSName(strings.Repeat("ab", 32))+":/var/mnt/pools/media") {
		t.Fatalf("mount: %q", run.cmds)
	}

	ExportsFile = filepath.Join(t.TempDir(), "maitre.exports")
	mountedDirs = map[string]bool{"/var/mnt/pools/media": true}
	_ = WriteExports(context.Background(), &fakeRunner{}, []string{"media"}, []string{"192.168.4.74"})
	if b, _ := os.ReadFile(ExportsFile); !strings.Contains(string(b), "192.168.4.74(rw,sync,no_subtree_check,no_root_squash,xprtsec=mtls,fsid=1000)") {
		t.Fatalf("exports:\n%s", b)
	}
}
