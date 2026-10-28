package authz_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/authz"
	"github.com/Onfire7/maitre-de-serveur/server/internal/identity"
	"github.com/Onfire7/maitre-de-serveur/server/internal/recsig"
	"github.com/Onfire7/maitre-de-serveur/server/internal/store"
)

type fleet struct {
	st                    *store.Store
	p                     *authz.Policy
	dev, rcv, a, b, c, xx *identity.Identity
}

func newID(t *testing.T) *identity.Identity {
	id, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func signed(t *testing.T, id *identity.Identity, rec *maitrev1.Record, at int64) *maitrev1.Record {
	t.Helper()
	if err := recsig.Sign(id, rec, at); err != nil {
		t.Fatal(err)
	}
	return rec
}

func body(t *testing.T, m proto.Message) []byte {
	b, err := proto.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func device(t *testing.T, id *identity.Identity, recovery bool, scope string) []byte {
	return body(t, &maitrev1.TrustedDevice{DeviceId: id.Fingerprint(), Name: "d", PublicKeyPem: string(id.PublicKeyPEM()), RecoveryOnly: recovery, NodeScope: scope})
}

func nodeBody(t *testing.T, id *identity.Identity, name string) []byte {
	return body(t, &maitrev1.Node{NodeId: id.Fingerprint(), Name: name, PublicKeyPem: string(id.PublicKeyPEM())})
}

// newFleet is Node a's database: a Trusted Device, the Recovery Phrase and
// Nodes a, b and c. xx is a stranger.
func newFleet(t *testing.T) *fleet {
	f := &fleet{dev: newID(t), rcv: newID(t), a: newID(t), b: newID(t), c: newID(t), xx: newID(t)}
	st, err := store.Open(filepath.Join(t.TempDir(), "fleet.db"), f.a.Fingerprint())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	f.st, f.p = st, authz.New(st, f.a.Fingerprint())
	ctx := context.Background()
	for _, r := range []*maitrev1.Record{
		signed(t, f.dev, &maitrev1.Record{Kind: store.KindTrustedDevice, Id: f.dev.Fingerprint(), OwnerNodeId: f.a.Fingerprint(), Version: 1, Body: device(t, f.dev, false, "")}, 1),
		signed(t, f.dev, &maitrev1.Record{Kind: store.KindTrustedDevice, Id: f.rcv.Fingerprint(), OwnerNodeId: f.a.Fingerprint(), Version: 1, Body: device(t, f.rcv, true, "")}, 1),
	} {
		if err := st.Seed(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	for _, n := range []*identity.Identity{f.a, f.b, f.c} {
		r := signed(t, n, &maitrev1.Record{Kind: store.KindNode, Id: n.Fingerprint(), OwnerNodeId: n.Fingerprint(), Version: 1, Body: nodeBody(t, n, "n")}, 1)
		if err := st.Seed(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func (f *fleet) admit(t *testing.T, rec *maitrev1.Record) error {
	t.Helper()
	cur, err := f.st.Raw(context.Background(), rec.GetKind(), rec.GetId())
	if errors.Is(err, store.ErrNotFound) {
		cur = nil
	} else if err != nil {
		t.Fatal(err)
	}
	return f.p.Admit(context.Background(), rec, cur)
}

func TestKinds(t *testing.T) {
	f := newFleet(t)
	b := f.b.Fingerprint()
	space := body(t, &maitrev1.Space{SpaceId: "media", Name: "Media"})
	svc := body(t, &maitrev1.ServiceSpec{Name: "web", NodeId: b})
	for _, c := range []struct {
		name string
		rec  *maitrev1.Record
		ok   bool
	}{
		{"space by a device", signed(t, f.dev, &maitrev1.Record{Kind: "space", Id: "media", OwnerNodeId: b, Version: 1, Body: space}, 10), true},
		{"space by a Node", signed(t, f.b, &maitrev1.Record{Kind: "space", Id: "media", OwnerNodeId: b, Version: 1, Body: space}, 10), false},
		{"space by a stranger", signed(t, f.xx, &maitrev1.Record{Kind: "space", Id: "media", OwnerNodeId: b, Version: 1, Body: space}, 10), false},
		{"space by the Recovery Phrase", signed(t, f.rcv, &maitrev1.Record{Kind: "space", Id: "media", OwnerNodeId: b, Version: 1, Body: space}, 10), false},
		{"unsigned space", &maitrev1.Record{Kind: "space", Id: "media", OwnerNodeId: b, Version: 1, Body: space}, false},
		{"grant by a Node", signed(t, f.b, &maitrev1.Record{Kind: "grant", Id: "g", OwnerNodeId: b, Version: 1}, 10), false},
		{"policy by a Node", signed(t, f.b, &maitrev1.Record{Kind: "update_policy", Id: "fleet", OwnerNodeId: b, Version: 1}, 10), false},
		{"service by its Node", signed(t, f.b, &maitrev1.Record{Kind: "service", Id: "web", OwnerNodeId: b, Version: 1, Body: svc}, 10), true},
		{"service for another Node", signed(t, f.c, &maitrev1.Record{Kind: "service", Id: "web", OwnerNodeId: f.c.Fingerprint(), Version: 1, Body: svc}, 10), false},
		{"service owned by someone else", signed(t, f.b, &maitrev1.Record{Kind: "service", Id: "web", OwnerNodeId: f.c.Fingerprint(), Version: 1, Body: svc}, 10), false},
		{"drive by its Node", signed(t, f.b, &maitrev1.Record{Kind: "drive", Id: "d1", OwnerNodeId: b, Version: 1}, 10), true},
		{"drive by another Node", signed(t, f.c, &maitrev1.Record{Kind: "drive", Id: "d1", OwnerNodeId: b, Version: 1}, 10), false},
		{"other kind by its Node", signed(t, f.b, &maitrev1.Record{Kind: "port_map", Id: "x", OwnerNodeId: b, Version: 1}, 10), true},
		{"other kind for another Node", signed(t, f.c, &maitrev1.Record{Kind: "port_map", Id: "x", OwnerNodeId: b, Version: 1}, 10), false},
		{"own Node record", signed(t, f.b, &maitrev1.Record{Kind: "node", Id: b, OwnerNodeId: b, Version: 2, Body: nodeBody(t, f.b, "renamed")}, 10), true},
		{"another Node's record", signed(t, f.c, &maitrev1.Record{Kind: "node", Id: b, OwnerNodeId: b, Version: 2, Body: nodeBody(t, f.b, "evil")}, 10), false},
		{"Node record by a device", signed(t, f.dev, &maitrev1.Record{Kind: "node", Id: b, OwnerNodeId: b, Version: 2, Deleted: true}, 10), true},
		{"a new Node by itself", signed(t, f.xx, &maitrev1.Record{Kind: "node", Id: f.xx.Fingerprint(), OwnerNodeId: f.xx.Fingerprint(), Version: 1, Body: nodeBody(t, f.xx, "x")}, 10), false},
		{"device by a device", signed(t, f.dev, &maitrev1.Record{Kind: "trusted_device", Id: f.xx.Fingerprint(), OwnerNodeId: b, Version: 1, Body: device(t, f.xx, false, "")}, 10), true},
		{"device by the Recovery Phrase", signed(t, f.rcv, &maitrev1.Record{Kind: "trusted_device", Id: f.xx.Fingerprint(), OwnerNodeId: b, Version: 1, Body: device(t, f.xx, false, "")}, 10), true},
		{"recovery-only device by the Recovery Phrase", signed(t, f.rcv, &maitrev1.Record{Kind: "trusted_device", Id: f.xx.Fingerprint(), OwnerNodeId: b, Version: 1, Body: device(t, f.xx, true, "")}, 10), false},
		{"device scoped to the signing Node", signed(t, f.b, &maitrev1.Record{Kind: "trusted_device", Id: f.xx.Fingerprint(), OwnerNodeId: b, Version: 1, Body: device(t, f.xx, false, b)}, 10), true},
		{"device scoped to another Node", signed(t, f.b, &maitrev1.Record{Kind: "trusted_device", Id: f.xx.Fingerprint(), OwnerNodeId: b, Version: 1, Body: device(t, f.xx, false, f.c.Fingerprint())}, 10), false},
		{"Fleet-wide device by a Node alone", signed(t, f.b, &maitrev1.Record{Kind: "trusted_device", Id: f.xx.Fingerprint(), OwnerNodeId: b, Version: 1, Body: device(t, f.xx, false, "")}, 10), false},
		{"device whose key is not its id", signed(t, f.dev, &maitrev1.Record{Kind: "trusted_device", Id: f.xx.Fingerprint(), OwnerNodeId: b, Version: 1,
			Body: body(t, &maitrev1.TrustedDevice{DeviceId: f.xx.Fingerprint(), PublicKeyPem: string(f.c.PublicKeyPEM())})}, 10), false},
	} {
		if err := f.admit(t, c.rec); (err == nil) != c.ok {
			t.Errorf("%s: admitted=%v (%v)", c.name, err == nil, err)
		} else if err != nil && !errors.Is(err, authz.ErrDenied) {
			t.Errorf("%s: %v is not ErrDenied", c.name, err)
		}
	}
}

func TestReplayAndRemoval(t *testing.T) {
	f := newFleet(t)
	ctx := context.Background()
	space := func(name string, at int64) *maitrev1.Record {
		return signed(t, f.dev, &maitrev1.Record{Kind: "space", Id: "media", OwnerNodeId: f.b.Fingerprint(), Version: uint64(at), UpdatedUnixMs: at,
			Body: body(t, &maitrev1.Space{SpaceId: "media", Name: name})}, at)
	}
	old, cur := space("old", 10), space("new", 20)
	if err := f.st.Seed(ctx, cur); err != nil {
		t.Fatal(err)
	}
	// A Node re-wraps the old signed body as a newer version.
	old.Version, old.UpdatedUnixMs = 99, 99
	if err := f.admit(t, old); err == nil {
		t.Fatal("replayed an older signature")
	}
	// A removed Node cannot bring itself back.
	gone := signed(t, f.dev, &maitrev1.Record{Kind: "node", Id: f.c.Fingerprint(), OwnerNodeId: f.c.Fingerprint(), Version: 2, Deleted: true}, 30)
	if err := f.st.Seed(ctx, gone); err != nil {
		t.Fatal(err)
	}
	back := signed(t, f.c, &maitrev1.Record{Kind: "node", Id: f.c.Fingerprint(), OwnerNodeId: f.c.Fingerprint(), Version: 3, Body: nodeBody(t, f.c, "back")}, 40)
	if err := f.admit(t, back); err == nil {
		t.Fatal("a removed Node signed itself back in")
	}
}

func TestPromotionNeedsSecondNode(t *testing.T) {
	f := newFleet(t)
	ctx := context.Background()
	b, c := f.b.Fingerprint(), f.c.Fingerprint()
	now := time.Now().UnixMilli()
	// The device's code at Node c: scoped to c, signed by c.
	scoped := signed(t, f.c, &maitrev1.Record{Kind: "trusted_device", Id: f.xx.Fingerprint(), OwnerNodeId: c, Version: 1, UpdatedUnixMs: now, Body: device(t, f.xx, false, c)}, now)
	if err := f.st.Seed(ctx, scoped); err != nil {
		t.Fatal(err)
	}
	promote := func(v *maitrev1.RecoveryVoucher, at int64) *maitrev1.Record {
		d := &maitrev1.TrustedDevice{DeviceId: f.xx.Fingerprint(), Name: "d", PublicKeyPem: string(f.xx.PublicKeyPEM()), Voucher: v}
		return signed(t, f.b, &maitrev1.Record{Kind: "trusted_device", Id: f.xx.Fingerprint(), OwnerNodeId: b, Version: 1, UpdatedUnixMs: at, Body: body(t, d)}, at)
	}
	voucher := &maitrev1.RecoveryVoucher{SignerKey: scoped.SignerKey, Signature: scoped.Signature, SignedUnixMs: scoped.SignedUnixMs, Body: scoped.Body}
	if err := f.admit(t, promote(voucher, now+1000)); err != nil {
		t.Fatalf("promotion with c's voucher: %v", err)
	}
	if err := f.admit(t, promote(nil, now+1000)); err == nil {
		t.Fatal("promotion without a voucher")
	}
	if err := f.admit(t, promote(voucher, now+int64(authz.PromoteWithin/time.Millisecond)+1000)); err == nil {
		t.Fatal("promotion with a stale voucher")
	}
	// b vouching for itself.
	self := signed(t, f.b, &maitrev1.Record{Kind: "trusted_device", Id: f.xx.Fingerprint(), Body: device(t, f.xx, false, b)}, now)
	if err := f.admit(t, promote(&maitrev1.RecoveryVoucher{SignerKey: self.SignerKey, Signature: self.Signature, SignedUnixMs: self.SignedUnixMs, Body: self.Body}, now+1000)); err == nil {
		t.Fatal("promotion vouched for by the same Node")
	}
}

func TestAdmitFromDevice(t *testing.T) {
	f := newFleet(t)
	ctx := context.Background()
	space := body(t, &maitrev1.Space{SpaceId: "media", Name: "Media"})
	if _, err := f.p.AdmitFromDevice(ctx, signed(t, f.dev, &maitrev1.Record{Kind: "space", Id: "media", Body: space}, 10)); err != nil {
		t.Fatalf("device: %v", err)
	}
	// A Node delivering its own signature through PutRecord.
	if _, err := f.p.AdmitFromDevice(ctx, signed(t, f.b, &maitrev1.Record{Kind: "drive", Id: "d"}, 10)); err == nil {
		t.Fatal("a Node signed a PutRecord")
	}
}
