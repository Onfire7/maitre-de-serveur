// Package authz decides which keys may sign which Records (decision 189,
// ADR 0008). A Node is not an administrator: it signs only facts about
// itself, and everything that grants power over the Fleet is signed by a
// Trusted Device. Every Node checks this for each Record it takes from a
// peer (store.Admit) or from a caller (PutRecord), so a compromised Node
// can relay the Fleet's Records but cannot forge them.
//
//   - trusted_device: a Trusted Device; the Recovery Phrase, for a new full
//     device; or a Node, for a device scoped to that Node, or promoted to
//     the whole Fleet with a second Node's voucher (decision 120).
//   - space, update_policy, tailscale, public_settings, grant: a Trusted
//     Device.
//   - node: a Trusted Device, or the Node itself once it has been
//     introduced and while it has not been removed.
//   - service: the Node that runs it, which may not take over another
//     Node's live Service of the same name.
//   - drive: the Node that owns it.
//   - anything else: a Trusted Device, or the owning Node.
//
// A replacement must be signed later than what it replaces, so a signed
// body cannot be replayed over a newer one. Records from before 189 carry
// no signature: a Node keeps those it has but accepts no new ones.
package authz

import (
	"context"
	"errors"
	"fmt"
	"time"

	"google.golang.org/protobuf/proto"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/identity"
	"github.com/Onfire7/maitre-de-serveur/server/internal/recsig"
	"github.com/Onfire7/maitre-de-serveur/server/internal/store"
)

// Record kinds this package names that live in packages it cannot import.
const (
	KindGrant          = "grant"
	kindSpace          = "space"
	kindPolicy         = "update_policy"
	kindTailscale      = "tailscale"
	kindPublicSettings = "public_settings"
	kindService        = "service"
	kindDrive          = "drive"
)

// PromoteWithin is how long a device scoped to one Node has to bring a
// recovery code to a second Node to be trusted Fleet-wide (decision 120).
const PromoteWithin = 24 * time.Hour

// DeviceOnly reports whether only a Trusted Device may sign kind.
func DeviceOnly(kind string) bool {
	switch kind {
	case store.KindTrustedDevice, kindSpace, kindPolicy, kindTailscale, kindPublicSettings, KindGrant:
		return true
	}
	return false
}

// ErrDenied wraps every refusal.
var ErrDenied = errors.New("not allowed")

// Policy checks Records against one Node's copy of the Fleet database.
type Policy struct {
	st   *store.Store
	self string
}

// New returns the policy of the Node self.
func New(st *store.Store, self string) *Policy { return &Policy{st: st, self: self} }

// Role is what a key is to this Node.
type Role int

const (
	Stranger Role = iota
	// Device is a Trusted Device this Node trusts in full.
	Device
	// Recovery is the Recovery Phrase: it may only add a full device.
	Recovery
	// Node is a Node of the Fleet.
	Node
)

// RoleOf returns what fingerprint is to this Node.
func (p *Policy) RoleOf(ctx context.Context, fp string) Role {
	var d maitrev1.TrustedDevice
	if _, err := p.st.Get(ctx, store.KindTrustedDevice, fp, &d); err == nil && d.GetDeviceId() == fp {
		switch {
		case d.GetNodeScope() != "" && d.GetNodeScope() != p.self:
			// Decision 120: trusted by another Node only.
		case d.GetRecoveryOnly():
			return Recovery
		default:
			return Device
		}
	}
	var n maitrev1.Node
	if _, err := p.st.Get(ctx, store.KindNode, fp, &n); err == nil && n.GetNodeId() == fp {
		return Node
	}
	return Stranger
}

// Admit is store.Admit: may rec, from a peer, replace cur (nil if none)?
func (p *Policy) Admit(ctx context.Context, rec, cur *maitrev1.Record) error {
	_, err := p.check(ctx, rec, cur, false)
	return err
}

// AdmitFromDevice is Admit for a change a caller asked for (PutRecord):
// it must be signed by a Trusted Device or the Recovery Phrase. Owner and
// version are not yet set. Returns the signer's role.
func (p *Policy) AdmitFromDevice(ctx context.Context, rec *maitrev1.Record) (Role, error) {
	cur, err := p.st.Raw(ctx, rec.GetKind(), rec.GetId())
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return Stranger, err
	}
	return p.check(ctx, rec, cur, true)
}

func (p *Policy) check(ctx context.Context, rec, cur *maitrev1.Record, fromDevice bool) (Role, error) {
	deny := func(format string, args ...any) (Role, error) {
		return Stranger, fmt.Errorf("%w: %s %s: %s", ErrDenied, rec.GetKind(), rec.GetId(), fmt.Sprintf(format, args...))
	}
	fp, err := recsig.Verify(rec)
	if errors.Is(err, recsig.ErrUnsigned) {
		return deny("unsigned; it was written before decision 189 or by an older Console")
	} else if err != nil {
		return deny("%v", err)
	}
	// An introduction (version 0) is replaced by the Node's own first
	// write, whose clock need not agree with the Console's.
	intro := cur != nil && cur.GetKind() == store.KindNode && cur.GetVersion() == 0
	if cur != nil && len(cur.GetSignature()) > 0 && !intro && rec.GetSignedUnixMs() <= cur.GetSignedUnixMs() {
		return deny("signed before the version it would replace")
	}
	role := p.RoleOf(ctx, fp)
	if fromDevice && role != Device && role != Recovery {
		return deny("must be signed by a Trusted Device")
	}
	ownNode := role == Node && rec.GetOwnerNodeId() == fp
	kind := rec.GetKind()
	switch {
	case kind == store.KindTrustedDevice:
		var d maitrev1.TrustedDevice
		if !rec.GetDeleted() {
			if err := proto.Unmarshal(rec.GetBody(), &d); err != nil {
				return deny("bad body: %v", err)
			}
			if d.GetDeviceId() != rec.GetId() {
				return deny("the device id does not match the record id")
			}
			if pub, err := identity.ParsePublicKeyPEM([]byte(d.GetPublicKeyPem())); err != nil || identity.Fingerprint(pub) != d.GetDeviceId() {
				return deny("the device's key does not match its id")
			}
		}
		switch role {
		case Device:
			return role, nil
		case Recovery:
			if rec.GetDeleted() || d.GetRecoveryOnly() || d.GetNodeScope() != "" {
				return deny("the Recovery Phrase may only add a full Trusted Device")
			}
			return role, nil
		case Node:
			if !ownNode || rec.GetDeleted() {
				return deny("a Node may only add a device through its recovery code")
			}
			if d.GetNodeScope() == fp {
				return role, nil
			}
			if d.GetNodeScope() == "" {
				if err := p.checkVoucher(ctx, rec, &d, fp); err != nil {
					return deny("promotion to the whole Fleet: %v", err)
				}
				return role, nil
			}
			return deny("a Node may only add a device scoped to itself")
		}
		return deny("signer is not trusted")
	case DeviceOnly(kind):
		if role == Device {
			return role, nil
		}
		return deny("must be signed by a Trusted Device")
	case kind == store.KindNode:
		if role == Device {
			return role, nil
		}
		if fp != rec.GetId() || !ownNode {
			return deny("a Node may only sign its own record")
		}
		if cur == nil || cur.GetDeleted() {
			return deny("the Node has not been added to the Fleet, or was removed")
		}
		if !rec.GetDeleted() {
			var n maitrev1.Node
			if err := proto.Unmarshal(rec.GetBody(), &n); err != nil {
				return deny("bad body: %v", err)
			}
			if pub, err := identity.ParsePublicKeyPEM([]byte(n.GetPublicKeyPem())); err != nil || n.GetNodeId() != fp || identity.Fingerprint(pub) != fp {
				return deny("the record does not describe its signer")
			}
		}
		return role, nil
	case kind == kindService:
		if !ownNode {
			return deny("a Service is signed by the Node that runs it")
		}
		if cur != nil && !cur.GetDeleted() && cur.GetOwnerNodeId() != fp {
			return deny("another Node's Service has this name")
		}
		if !rec.GetDeleted() {
			var s maitrev1.ServiceSpec
			if err := proto.Unmarshal(rec.GetBody(), &s); err != nil {
				return deny("bad body: %v", err)
			}
			if s.GetNodeId() != fp {
				return deny("a Node may only sign its own Services")
			}
		}
		return role, nil
	case kind == kindDrive:
		if ownNode {
			return role, nil
		}
		return deny("a Drive is signed by the Node that has it")
	default:
		if role == Device || ownNode {
			return role, nil
		}
		return deny("must be signed by a Trusted Device or the owning Node")
	}
}

// checkVoucher checks a Fleet-wide device signed by Node fp carries another
// Node's signed record of the same device scoped to that Node, signed no
// more than PromoteWithin earlier (decision 120).
func (p *Policy) checkVoucher(ctx context.Context, rec *maitrev1.Record, d *maitrev1.TrustedDevice, fp string) error {
	v := d.GetVoucher()
	if v == nil {
		return errors.New("no voucher from a second Node")
	}
	vrec := &maitrev1.Record{Kind: store.KindTrustedDevice, Id: rec.GetId(), Body: v.GetBody(), SignerKey: v.GetSignerKey(), Signature: v.GetSignature(), SignedUnixMs: v.GetSignedUnixMs()}
	vfp, err := recsig.Verify(vrec)
	if err != nil {
		return fmt.Errorf("voucher: %w", err)
	}
	if vfp == fp || p.RoleOf(ctx, vfp) != Node {
		return errors.New("the voucher is not from another Node of the Fleet")
	}
	var scoped maitrev1.TrustedDevice
	if err := proto.Unmarshal(v.GetBody(), &scoped); err != nil {
		return fmt.Errorf("voucher: %w", err)
	}
	if scoped.GetDeviceId() != d.GetDeviceId() || scoped.GetPublicKeyPem() != d.GetPublicKeyPem() || scoped.GetNodeScope() != vfp {
		return errors.New("the voucher is for another device")
	}
	if age := time.Duration(rec.GetSignedUnixMs()-v.GetSignedUnixMs()) * time.Millisecond; age < 0 || age > PromoteWithin {
		return errors.New("the voucher is too old")
	}
	return nil
}

// SignedByDevice reports whether a stored Record is signed by a key this
// Node trusts as a full Trusted Device now.
func (p *Policy) SignedByDevice(ctx context.Context, rec *maitrev1.Record) bool {
	fp, err := recsig.Verify(rec)
	return err == nil && p.RoleOf(ctx, fp) == Device
}

// SignedBySelf reports whether a stored Record is signed by this Node.
func (p *Policy) SignedBySelf(rec *maitrev1.Record) bool {
	fp, err := recsig.Verify(rec)
	return err == nil && fp == p.self
}
