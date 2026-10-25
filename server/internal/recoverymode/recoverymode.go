// Package recoverymode is decision 101: a Node rebooted into recovery from
// its own keyboard runs with networking off and shows a one-time code; on
// the next normal boot the code is honoured for 15 minutes on a dedicated
// listener that accepts any client key, adding the caller as a Trusted
// Device. Nothing on the network can start recovery.
package recoverymode

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"connectrpc.com/connect/v2"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1/maitrev1connect"
	"github.com/Onfire7/maitre-de-serveur/server/internal/identity"
	"github.com/Onfire7/maitre-de-serveur/server/internal/peer"
	"github.com/Onfire7/maitre-de-serveur/server/internal/store"
)

const (
	// RequestFile, in the state directory, asks the next boot to enter recovery.
	RequestFile = "recovery-requested"
	// CodeFile holds the salted hash of the one-time code and when it was made.
	CodeFile = "recovery-code.json"
	// Window is how long after a normal boot the code is honoured.
	Window = 15 * time.Minute
	// MaxAttempts before the code is discarded.
	MaxAttempts = 5
	// Port of the recovery listener.
	Port = 7444
)

type codeRecord struct {
	Salt          string `json:"salt"`
	Hash          string `json:"hash"`
	CreatedUnixMs int64  `json:"created_unix_ms"`
}

// Requested reports whether the last session asked for recovery, and clears
// the request so a crash loop cannot keep a Node offline.
func Requested(stateDir string) bool {
	p := filepath.Join(stateDir, RequestFile)
	if _, err := os.Stat(p); err != nil {
		return false
	}
	_ = os.Remove(p)
	return true
}

// Request asks for recovery on the next boot.
func Request(stateDir string) error {
	return os.WriteFile(filepath.Join(stateDir, RequestFile), []byte(time.Now().Format(time.RFC3339)+"\n"), 0o600)
}

// NewCode makes an 8-digit code, stores its salted hash, and returns it for
// the screen. It is never written in clear.
func NewCode(stateDir string) (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	code := ""
	for _, x := range b {
		code += fmt.Sprint(int(x) % 10)
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	rec := codeRecord{Salt: hex.EncodeToString(salt), Hash: hashCode(salt, code), CreatedUnixMs: time.Now().UnixMilli()}
	raw, _ := json.Marshal(rec)
	if err := os.WriteFile(filepath.Join(stateDir, CodeFile), raw, 0o600); err != nil {
		return "", err
	}
	return code, nil
}

func hashCode(salt []byte, code string) string {
	h := sha256.New()
	h.Write([]byte("maitre-recovery-code-v1\x00"))
	h.Write(salt)
	h.Write([]byte(code))
	return hex.EncodeToString(h.Sum(nil))
}

// Pending reports whether a code is waiting to be honoured (made less than
// a day ago; older ones are discarded).
func Pending(stateDir string) bool {
	rec, err := read(stateDir)
	if err != nil {
		return false
	}
	if time.Since(time.UnixMilli(rec.CreatedUnixMs)) > 24*time.Hour {
		_ = os.Remove(filepath.Join(stateDir, CodeFile))
		return false
	}
	return true
}

func read(stateDir string) (*codeRecord, error) {
	raw, err := os.ReadFile(filepath.Join(stateDir, CodeFile))
	if err != nil {
		return nil, err
	}
	var rec codeRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return nil, err
	}
	return &rec, nil
}

// Discard forgets the pending code.
func Discard(stateDir string) { _ = os.Remove(filepath.Join(stateDir, CodeFile)) }

// Service answers Authorize during the window.
type Service struct {
	stateDir string
	st       *store.Store
	self     string
	fleet    *maitrev1.Fleet
	// OnAdded runs after a device was added (refresh trust, push to peers, close).
	OnAdded func(rec *maitrev1.Record)

	mu       sync.Mutex
	attempts int
	done     bool
}

var _ maitrev1connect.RecoveryServiceHandler = (*Service)(nil)

// NewService builds the recovery listener's handler.
func NewService(stateDir string, st *store.Store, self string, fleet *maitrev1.Fleet) *Service {
	return &Service{stateDir: stateDir, st: st, self: self, fleet: fleet}
}

// Authorize implements RecoveryService.
func (s *Service) Authorize(ctx context.Context, req *maitrev1.AuthorizeRequest) (*maitrev1.AuthorizeResponse, error) {
	p, ok := peer.From(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, "no client key")
	}
	pub, err := identity.ParsePublicKeyPEM([]byte(req.GetPublicKeyPem()))
	if err != nil || identity.Fingerprint(pub) != p.Fingerprint {
		return nil, connect.NewError(connect.CodeInvalidArgument, "public_key_pem must be the key of this connection")
	}
	if req.GetName() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "a device name is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		return nil, connect.NewError(connect.CodeFailedPrecondition, "the recovery window is closed")
	}
	rec, err := read(s.stateDir)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, "no recovery code is pending")
	}
	salt, _ := hex.DecodeString(rec.Salt)
	s.attempts++
	if subtle.ConstantTimeCompare([]byte(hashCode(salt, req.GetCode())), []byte(rec.Hash)) != 1 {
		if s.attempts >= MaxAttempts {
			Discard(s.stateDir)
			s.done = true
			return nil, connect.NewError(connect.CodePermissionDenied, "wrong code; the recovery window is now closed")
		}
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Sprintf("wrong code (%d attempt(s) left)", MaxAttempts-s.attempts))
	}
	device := &maitrev1.TrustedDevice{DeviceId: p.Fingerprint, Name: req.GetName(), PublicKeyPem: req.GetPublicKeyPem(), AddedUnixMs: time.Now().UnixMilli()}
	out, err := s.st.Put(ctx, store.KindTrustedDevice, device.DeviceId, device, true)
	if err != nil {
		return nil, connect.Errorf(connect.CodeInternal, "%v", err)
	}
	Discard(s.stateDir)
	s.done = true
	if s.OnAdded != nil {
		s.OnAdded(out)
	}
	return &maitrev1.AuthorizeResponse{FleetId: s.fleet.GetFleetId(), FleetName: s.fleet.GetName(), NodeId: s.self}, nil
}

// ErrClosed is returned by Wait when the window ended without a device.
var ErrClosed = errors.New("recovery window closed")
