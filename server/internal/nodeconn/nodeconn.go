// Package nodeconn remembers how to reach a Node, so the CLI is told once
// instead of every time.
//
// This is the Desktop's `KnownNode` in a file: a Node's id, its name and
// the host:port ways to reach it, saved on the first connection that
// works. The Desktop keeps the same three in its own console.json and
// learns them from the Fleet's Node records; so does this, which is why
// one connection to any Node of a Fleet is enough to know them all.
//
// What is saved is only what is already public: a Node's id is its key's
// fingerprint, and a fingerprint is what a caller pins to. The private
// half is never copied here — the identity is kept as the path to the
// directory that holds it, so the key stays wherever its owner put it.
//
// The id is the pin, and that is the point: after the first connection a
// caller needs no trust directory, because the Node's own id is what the
// trust list checks the server's key against.
package nodeconn

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"
)

// File is the name of the store inside the state directory.
const File = "nodes.json"

// maxAddresses is how many ways to one Node are kept. A way that failed is
// demoted rather than forgotten (decision 284), but a laptop that moves
// enough would otherwise collect every address it ever had.
const maxAddresses = 8

// Known is a Node this CLI knows how to reach.
type Known struct {
	// NodeID is the Node's key fingerprint, which is what a caller pins
	// the TLS connection to.
	NodeID string `json:"node_id"`
	Name   string `json:"name"`
	// Addresses are host:port, the one that answered last first.
	Addresses []string `json:"addresses"`
	// Identity is the directory holding the caller's own key, not the key.
	Identity     string `json:"identity,omitempty"`
	LastSeenUnix int64  `json:"last_seen_unix,omitempty"`
}

// Store is the saved set of Nodes and which was used last.
type Store struct {
	Last  string  `json:"last,omitempty"`
	Nodes []Known `json:"nodes,omitempty"`

	path string
}

// Load reads the store from the state directory. A missing file is not an
// error: it is the first run.
func Load(stateDir string) (*Store, error) {
	s := &Store{path: filepath.Join(stateDir, File)}
	raw, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(raw, s); err != nil {
		// A corrupt file must not lock the user out of their own Nodes:
		// the connection can always be given again on the command line.
		return &Store{path: s.path}, nil
	}
	return s, nil
}

// Path is where this store is kept, for a message that names it.
func (s *Store) Path() string { return s.path }

// Save writes the store, creating the state directory if it is new.
func (s *Store) Save() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, append(raw, '\n'), 0o600)
}

// Find returns the Node named by q: its name, the start of its id, or one
// of its addresses, whichever matches. An empty q means the last one used.
func (s *Store) Find(q string) *Known {
	if q == "" {
		q = s.Last
	}
	if q == "" {
		return nil
	}
	lower := strings.ToLower(q)
	for i := range s.Nodes {
		if strings.EqualFold(s.Nodes[i].Name, q) {
			return &s.Nodes[i]
		}
	}
	for i := range s.Nodes {
		if strings.HasPrefix(s.Nodes[i].NodeID, lower) {
			return &s.Nodes[i]
		}
	}
	for i := range s.Nodes {
		if slices.Contains(s.Nodes[i].Addresses, q) {
			return &s.Nodes[i]
		}
	}
	return nil
}

// Remember saves what a connection taught us. The address that answered
// goes to the front of that Node's list and the others are kept behind it;
// used marks this Node as the one a bare command should use next.
func (s *Store) Remember(k Known, used bool) {
	if k.NodeID == "" {
		return
	}
	i := slices.IndexFunc(s.Nodes, func(n Known) bool { return n.NodeID == k.NodeID })
	if i < 0 {
		s.Nodes = append(s.Nodes, Known{NodeID: k.NodeID})
		i = len(s.Nodes) - 1
	}
	n := &s.Nodes[i]
	if k.Name != "" {
		n.Name = k.Name
	}
	if k.Identity != "" {
		n.Identity = k.Identity
	}
	if k.LastSeenUnix != 0 {
		n.LastSeenUnix = k.LastSeenUnix
	}
	for j := len(k.Addresses) - 1; j >= 0; j-- {
		n.Addresses = promote(n.Addresses, k.Addresses[j])
	}
	if len(n.Addresses) > maxAddresses {
		n.Addresses = n.Addresses[:maxAddresses]
	}
	if used {
		s.Last = n.Key()
	}
}

// promote puts addr at the front of addrs, keeping the rest in order.
func promote(addrs []string, addr string) []string {
	if addr == "" {
		return addrs
	}
	out := make([]string, 0, len(addrs)+1)
	out = append(out, addr)
	for _, a := range addrs {
		if a != addr {
			out = append(out, a)
		}
	}
	return out
}

// Forget drops the Node named by q and reports whether it was there.
func (s *Store) Forget(q string) bool {
	k := s.Find(q)
	if k == nil {
		return false
	}
	id, key := k.NodeID, k.Key()
	s.Nodes = slices.DeleteFunc(s.Nodes, func(n Known) bool { return n.NodeID == id })
	if s.Last == key {
		s.Last = ""
	}
	return true
}

// Key is how this Node is referred to on the command line: its name when
// it has one, otherwise the short form of its id.
func (k *Known) Key() string {
	if k.Name != "" {
		return k.Name
	}
	return k.Short()
}

// Short is the first twelve characters of the id, which is how every other
// surface prints a fingerprint.
func (k *Known) Short() string {
	if len(k.NodeID) > 12 {
		return k.NodeID[:12]
	}
	return k.NodeID
}

// LastSeen is when this Node last answered, or the zero time.
func (k *Known) LastSeen() time.Time {
	if k.LastSeenUnix == 0 {
		return time.Time{}
	}
	return time.Unix(k.LastSeenUnix, 0)
}

// DesktopIdentityDir is where Maître Desktop keeps this computer's device
// key. The CLI falls back to it so that a machine whose Console is already
// a Trusted Device does not have to pair a second identity to run a
// command — it is the same person on the same computer, and the Desktop's
// own data directory is as private as anything the CLI would write.
func DesktopIdentityDir() string {
	const app = "dev.maitre.desktop"
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", app, "device")
	case "windows":
		if dir := os.Getenv("APPDATA"); dir != "" {
			return filepath.Join(dir, app, "device")
		}
		return filepath.Join(home, "AppData", "Roaming", app, "device")
	default:
		if dir := os.Getenv("XDG_DATA_HOME"); dir != "" {
			return filepath.Join(dir, app, "device")
		}
		return filepath.Join(home, ".local", "share", app, "device")
	}
}
