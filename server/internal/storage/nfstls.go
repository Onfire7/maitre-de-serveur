package storage

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/identity"
	"github.com/Onfire7/maitre-de-serveur/server/internal/store"
)

// NFS over TLS (decision 188): every Pool is exported and mounted with
// xprtsec=mtls. The kernel hands the handshake to tlshd, which presents
// this Node's NFS certificate and accepts only certificates in the trust
// store, which holds the Fleet's other Nodes' certificates from their Node
// records. A client also checks the server's name, so peers are mounted by
// identity.NFSName, which /etc/hosts maps to their current address.

// Variables for tests. TLSDir is under /etc/pki because tlshd's SELinux
// domain (ktlshd_t) may read cert_t files and little else; image/tlshd.conf
// names the files in it.
var (
	TLSDir    = "/etc/pki/tlshd"
	HostsFile = "/etc/hosts"
)

const (
	tlsCertFile  = "node.crt"
	tlsKeyFile   = "node.key"
	tlsTrustFile = "fleet.pem"
	hostsBegin   = "# BEGIN maitre: the Fleet's Nodes by NFS name (decision 188)"
	hostsEnd     = "# END maitre"
)

// WriteTLSIdentity writes this Node's NFS certificate and private key where
// tlshd reads them. tlshd reads them at each handshake; no restart needed.
func WriteTLSIdentity(certPEM, keyPEM []byte) error {
	if err := os.MkdirAll(TLSDir, 0o755); err != nil {
		return err
	}
	if err := writeIfChanged(filepath.Join(TLSDir, tlsCertFile), certPEM, 0o644); err != nil {
		return err
	}
	return writeIfChanged(filepath.Join(TLSDir, tlsKeyFile), keyPEM, 0o600)
}

// WriteNFSPeers writes the trust store from the other Nodes' records (only
// certificates its own key signed) and maps each peer's NFS name
// to its address in /etc/hosts. addrs is the best address per Node id.
func WriteNFSPeers(ctx context.Context, st *store.Store, addrs map[string]string) error {
	nodes, err := store.ListLive(ctx, st, store.KindNode, func() *maitrev1.Node { return &maitrev1.Node{} })
	if err != nil {
		return err
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].GetNodeId() < nodes[j].GetNodeId() })
	var trust strings.Builder
	var hosts []string
	for _, n := range nodes {
		id := n.GetNodeId()
		if id == st.NodeID() {
			continue
		}
		if nfsCertificateOK(n) {
			trust.WriteString(n.GetNfsCertificatePem())
		}
		if h := hostOf(addrs[id]); h != "" {
			hosts = append(hosts, h+" "+identity.NFSName(id))
		}
	}
	if err := os.MkdirAll(TLSDir, 0o755); err != nil {
		return err
	}
	if err := writeIfChanged(filepath.Join(TLSDir, tlsTrustFile), []byte(trust.String()), 0o644); err != nil {
		return err
	}
	return writeHostsBlock(hosts)
}

// HasNFSCertificate reports whether a peer's Node record carries a valid NFS
// certificate; Nodes from before decision 188 do not, and cannot be mounted.
func HasNFSCertificate(ctx context.Context, st *store.Store, nodeID string) bool {
	var n maitrev1.Node
	if _, err := st.Get(ctx, store.KindNode, nodeID, &n); err != nil {
		return false
	}
	return n.GetNodeId() == nodeID && nfsCertificateOK(&n)
}

// nfsCertificateOK checks a Node record's NFS certificate against the
// record's own public key, which must be the key its id names.
func nfsCertificateOK(n *maitrev1.Node) bool {
	pub, err := identity.ParsePublicKeyPEM([]byte(n.GetPublicKeyPem()))
	if err != nil || identity.Fingerprint(pub) != n.GetNodeId() {
		return false
	}
	return identity.CheckNFSCertificate([]byte(n.GetNfsCertificatePem()), n.GetNfsCertificateSignature(), pub) == nil
}

func hostOf(addr string) string {
	if h, _, err := net.SplitHostPort(addr); err == nil {
		addr = h
	}
	if ip := net.ParseIP(addr); ip != nil {
		return ip.String()
	}
	return ""
}

// writeHostsBlock replaces the block between hostsBegin and hostsEnd (or
// appends one), leaving the rest of the file alone.
func writeHostsBlock(lines []string) error {
	cur, err := os.ReadFile(HostsFile)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	var keep []string
	in := false
	for _, l := range strings.Split(strings.TrimRight(string(cur), "\n"), "\n") {
		switch {
		case l == hostsBegin:
			in = true
		case l == hostsEnd && in:
			in = false
		case !in && (l != "" || len(keep) > 0):
			keep = append(keep, l)
		}
	}
	out := strings.Join(keep, "\n")
	if len(lines) > 0 {
		if out != "" {
			out += "\n"
		}
		out += hostsBegin + "\n" + strings.Join(lines, "\n") + "\n" + hostsEnd
	}
	if out != "" {
		out += "\n"
	}
	return writeIfChanged(HostsFile, []byte(out), 0o644)
}

// writeIfChanged rewrites a file in place only when its content differs.
// In place, not by rename, so the file keeps its SELinux label (/etc/hosts
// is net_conf_t; a new file would be etc_t).
func writeIfChanged(path string, data []byte, mode os.FileMode) error {
	cur, err := os.ReadFile(path)
	if err == nil && string(cur) == string(data) {
		return nil
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	return f.Close()
}
