// Package store is the Fleet database on one Node: SQLite holding Records
// (ADR 0003). Milestone 1 ships the single-Node form; the Record shape and
// the ownership rule are final here so Milestone 3 only adds exchange.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"
	_ "modernc.org/sqlite"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/identity"
	"github.com/Onfire7/maitre-de-serveur/server/internal/recsig"
)

// Record kinds. Bodies are the proto messages of the same name in fleet.proto.
const (
	KindFleet         = "fleet"
	KindNode          = "node"
	KindTrustedDevice = "trusted_device"
)

// ErrNotOwner is returned by Put when another Node owns the Record.
var ErrNotOwner = errors.New("record is owned by another node")

// ErrNotFound is returned by Get for a missing or deleted Record.
var ErrNotFound = errors.New("record not found")

// Store is one Node's copy of the Fleet database.
type Store struct {
	db     *sql.DB
	nodeID string
	now    func() time.Time
	signer *identity.Identity

	// Admit decides whether a Record from another Node may replace cur
	// (nil when there is none); see package authz (decision 189). Nil
	// admits everything, for tests and tools.
	Admit func(ctx context.Context, rec, cur *maitrev1.Record) error
}

// SignWith makes this Store sign every Record it writes itself with id,
// this Node's identity (decision 189).
func (s *Store) SignWith(id *identity.Identity) { s.signer = id }

// Open opens or creates the database at path. nodeID is this Node's identity
// and becomes the owner of Records written through Put.
func Open(path, nodeID string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db, nodeID: nodeID, now: time.Now}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// NodeID is the owner used for local writes.
func (s *Store) NodeID() string { return s.nodeID }

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS records (
  kind            TEXT    NOT NULL,
  id              TEXT    NOT NULL,
  owner_node_id   TEXT    NOT NULL,
  version         INTEGER NOT NULL,
  updated_unix_ms INTEGER NOT NULL,
  body            BLOB    NOT NULL,
  deleted         INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (kind, id)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS records_owner ON records(owner_node_id, version);
CREATE TABLE IF NOT EXISTS outbox (
  seq             INTEGER PRIMARY KEY AUTOINCREMENT,
  owner_node_id   TEXT    NOT NULL,
  kind            TEXT    NOT NULL,
  id              TEXT    NOT NULL,
  body            BLOB    NOT NULL,
  deleted         INTEGER NOT NULL DEFAULT 0,
  created_unix_ms INTEGER NOT NULL
);
`)
	if err != nil {
		return err
	}
	// Decision 189: signatures. Rows from before have none.
	for _, t := range []string{"records", "outbox"} {
		for _, c := range []string{"signer_key BLOB NOT NULL DEFAULT x''", "signature BLOB NOT NULL DEFAULT x''", "signed_unix_ms INTEGER NOT NULL DEFAULT 0"} {
			if err := s.addColumn(t, c); err != nil {
				return err
			}
		}
	}
	return nil
}

// addColumn adds a column unless the table has it.
func (s *Store) addColumn(table, def string) error {
	name := strings.Fields(def)[0]
	rows, err := s.db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return err
		}
		if n == name {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()
	_, err = s.db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + def)
	return err
}

// Snapshot writes a consistent copy of the database to path (SQLite's
// VACUUM INTO), for the config backup.
func (s *Store) Snapshot(ctx context.Context, path string) error {
	_ = os.Remove(path)
	_, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, path)
	return err
}

// Introduce stores a Node record on behalf of the Node it describes, owned
// by that Node at version 0, so the Node's own first write (version 1)
// supersedes it everywhere. This is how a Console or a peer tells a Node
// about another Node before they have met. Returns false if a record for
// that Node already exists.
func (s *Store) Introduce(ctx context.Context, node *maitrev1.Node) (bool, error) {
	raw, err := proto.Marshal(node)
	if err != nil {
		return false, err
	}
	return s.IntroduceSigned(ctx, &maitrev1.Record{Kind: KindNode, Id: node.GetNodeId(), Body: raw})
}

// IntroduceSigned is Introduce with a Trusted Device's signed Node record
// (decision 189), kept with its signature.
func (s *Store) IntroduceSigned(ctx context.Context, rec *maitrev1.Record) (bool, error) {
	body := rec.GetBody()
	if body == nil {
		body = []byte{}
	}
	res, err := s.db.ExecContext(ctx, `
INSERT OR IGNORE INTO records (kind, id, owner_node_id, version, updated_unix_ms, body, deleted, signer_key, signature, signed_unix_ms)
VALUES (?, ?, ?, 0, ?, ?, 0, ?, ?, ?)`, KindNode, rec.GetId(), rec.GetId(), s.now().UnixMilli(), body, nz(rec.GetSignerKey()), nz(rec.GetSignature()), rec.GetSignedUnixMs())
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// Seed stores a Record received with the Node's provisioning as it is
// (decision 189): the Console that paired with the Installer vouches for
// it. A Record already held is replaced only by a newer one.
func (s *Store) Seed(ctx context.Context, rec *maitrev1.Record) error {
	_, err := s.apply(ctx, rec, nil)
	return err
}

// Tombstone deletes (kind, id) on behalf of its owner: the owner stays, the
// version advances, so every Node applies it even against the owner's own
// later writes. For removing a Node that is gone for good (Milestone 4
// guard): its Record is owned by itself and nobody else could delete it.
//
// signed carries a Trusted Device's signature for the tombstone (decision
// 189); nil leaves it unsigned.
func (s *Store) Tombstone(ctx context.Context, kind, id string, signed *maitrev1.Record) (*maitrev1.Record, error) {
	cur, err := s.getRaw(ctx, kind, id)
	if err != nil {
		return nil, err
	}
	rec := &maitrev1.Record{Kind: kind, Id: id, OwnerNodeId: cur.OwnerNodeId, Version: cur.Version + 1, UpdatedUnixMs: s.now().UnixMilli(), Body: []byte{}, Deleted: true,
		SignerKey: signed.GetSignerKey(), Signature: signed.GetSignature(), SignedUnixMs: signed.GetSignedUnixMs()}
	if _, err := s.db.ExecContext(ctx, `UPDATE records SET version=?, updated_unix_ms=?, body=?, deleted=1, signer_key=?, signature=?, signed_unix_ms=? WHERE kind=? AND id=?`,
		rec.Version, rec.UpdatedUnixMs, rec.Body, nz(rec.SignerKey), nz(rec.Signature), rec.SignedUnixMs, kind, id); err != nil {
		return nil, err
	}
	return rec, nil
}

// Owner returns who owns (kind, id), or "" if the Record does not exist.
func (s *Store) Owner(ctx context.Context, kind, id string) (string, error) {
	var owner string
	err := s.db.QueryRowContext(ctx, `SELECT owner_node_id FROM records WHERE kind=? AND id=?`, kind, id).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return owner, err
}

// Queued is a write waiting for its owner to come back (ADR 0003).
type Queued struct {
	Seq   int64
	Owner string
	// Record is the write as it was asked for, signature included.
	Record *maitrev1.Record
}

// Enqueue stores a write for an offline owner.
func (s *Store) Enqueue(ctx context.Context, owner string, rec *maitrev1.Record) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO outbox (owner_node_id, kind, id, body, deleted, created_unix_ms, signer_key, signature, signed_unix_ms) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		owner, rec.GetKind(), rec.GetId(), nz(rec.GetBody()), boolInt(rec.GetDeleted()), s.now().UnixMilli(), nz(rec.GetSignerKey()), nz(rec.GetSignature()), rec.GetSignedUnixMs())
	return err
}

// Pending lists queued writes, oldest first; owner "" means all.
func (s *Store) Pending(ctx context.Context, owner string) ([]Queued, error) {
	q := `SELECT seq, owner_node_id, kind, id, body, deleted, signer_key, signature, signed_unix_ms FROM outbox`
	var args []any
	if owner != "" {
		q += ` WHERE owner_node_id=?`
		args = append(args, owner)
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY seq`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Queued
	for rows.Next() {
		q := Queued{Record: &maitrev1.Record{}}
		r := q.Record
		var del int
		if err := rows.Scan(&q.Seq, &q.Owner, &r.Kind, &r.Id, &r.Body, &del, &r.SignerKey, &r.Signature, &r.SignedUnixMs); err != nil {
			return nil, err
		}
		r.Deleted = del != 0
		out = append(out, q)
	}
	return out, rows.Err()
}

// Dequeue removes a delivered write.
func (s *Store) Dequeue(ctx context.Context, seq int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM outbox WHERE seq=?`, seq)
	return err
}

// Put writes body as the Record (kind, id) owned by this Node. A Record
// another Node owns is refused with ErrNotOwner unless fleetWide is true, in
// which case this Node takes ownership (newest-wins, ADR 0003).
func (s *Store) Put(ctx context.Context, kind, id string, body proto.Message, fleetWide bool) (*maitrev1.Record, error) {
	raw, err := proto.Marshal(body)
	if err != nil {
		return nil, err
	}
	return s.write(ctx, kind, id, raw, false, fleetWide)
}

// PutRaw is Put with an already-serialized body.
func (s *Store) PutRaw(ctx context.Context, kind, id string, raw []byte, fleetWide bool) (*maitrev1.Record, error) {
	return s.write(ctx, kind, id, raw, false, fleetWide)
}

// Delete writes a tombstone for (kind, id).
func (s *Store) Delete(ctx context.Context, kind, id string, fleetWide bool) (*maitrev1.Record, error) {
	return s.write(ctx, kind, id, nil, true, fleetWide)
}

// ErrStale is a signed write signed no later than what it would replace.
var ErrStale = errors.New("the change was signed before the version it would replace; check this device's clock")

// PutSigned writes a Record whose content someone else signed, a Trusted
// Device (decision 189): kind, id, body, deleted and the signature come
// from signed; owner and version are this Node's, as for Put.
func (s *Store) PutSigned(ctx context.Context, signed *maitrev1.Record, fleetWide bool) (*maitrev1.Record, error) {
	return s.writeRec(ctx, &maitrev1.Record{Kind: signed.GetKind(), Id: signed.GetId(), Body: signed.GetBody(), Deleted: signed.GetDeleted(),
		SignerKey: signed.GetSignerKey(), Signature: signed.GetSignature(), SignedUnixMs: signed.GetSignedUnixMs()}, fleetWide)
}

func (s *Store) write(ctx context.Context, kind, id string, raw []byte, deleted, fleetWide bool) (*maitrev1.Record, error) {
	return s.writeRec(ctx, &maitrev1.Record{Kind: kind, Id: id, Body: raw, Deleted: deleted}, fleetWide)
}

// writeRec writes rec as this Node's next version. Without a signature,
// this Node signs it (when it has a signer), later than the version it
// replaces even if the clock went back.
func (s *Store) writeRec(ctx context.Context, rec *maitrev1.Record, fleetWide bool) (*maitrev1.Record, error) {
	if rec.Body == nil || rec.Deleted {
		rec.Body = []byte{}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var owner string
	var version uint64
	var signedAt int64
	err = tx.QueryRowContext(ctx, `SELECT owner_node_id, version, signed_unix_ms FROM records WHERE kind=? AND id=?`, rec.Kind, rec.Id).Scan(&owner, &version, &signedAt)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return nil, err
	case owner != s.nodeID && !fleetWide:
		return nil, fmt.Errorf("%w: %s", ErrNotOwner, owner)
	}
	rec.OwnerNodeId, rec.Version, rec.UpdatedUnixMs = s.nodeID, version+1, s.now().UnixMilli()
	switch {
	case len(rec.Signature) > 0:
		if rec.SignedUnixMs <= signedAt {
			return nil, ErrStale
		}
	case s.signer != nil:
		if err := recsig.Sign(s.signer, rec, max(rec.UpdatedUnixMs, signedAt+1)); err != nil {
			return nil, err
		}
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO records (kind, id, owner_node_id, version, updated_unix_ms, body, deleted, signer_key, signature, signed_unix_ms)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(kind, id) DO UPDATE SET
  owner_node_id=excluded.owner_node_id, version=excluded.version,
  updated_unix_ms=excluded.updated_unix_ms, body=excluded.body, deleted=excluded.deleted,
  signer_key=excluded.signer_key, signature=excluded.signature, signed_unix_ms=excluded.signed_unix_ms`,
		rec.Kind, rec.Id, rec.OwnerNodeId, rec.Version, rec.UpdatedUnixMs, rec.Body, boolInt(rec.Deleted), nz(rec.SignerKey), nz(rec.Signature), rec.SignedUnixMs); err != nil {
		return nil, err
	}
	return rec, tx.Commit()
}

// ResignOwn signs, as a new version, this Node's own Records from before
// decision 189 whose kind sign accepts, so peers keep accepting their
// changes. Returns the rewritten Records.
func (s *Store) ResignOwn(ctx context.Context, sign func(kind string) bool) ([]*maitrev1.Record, error) {
	if s.signer == nil {
		return nil, nil
	}
	all, err := s.List(ctx, "")
	if err != nil {
		return nil, err
	}
	var out []*maitrev1.Record
	for _, r := range all {
		if r.OwnerNodeId != s.nodeID || len(r.Signature) > 0 || !sign(r.Kind) {
			continue
		}
		rec, err := s.writeRec(ctx, &maitrev1.Record{Kind: r.Kind, Id: r.Id, Body: r.Body, Deleted: r.Deleted}, false)
		if err != nil {
			return out, err
		}
		out = append(out, rec)
	}
	return out, nil
}

// nz turns nil into an empty blob: the columns are NOT NULL.
func nz(b []byte) []byte {
	if b == nil {
		return []byte{}
	}
	return b
}

// Apply stores a Record received from another Node verbatim if it is newer
// than what we hold: higher version from the same owner, or any version from
// a different owner with a later timestamp (ties broken by owner ID so every
// Node converges on the same answer). Returns true if applied.
func (s *Store) Apply(ctx context.Context, rec *maitrev1.Record) (bool, error) {
	return s.apply(ctx, rec, s.Admit)
}

func (s *Store) apply(ctx context.Context, rec *maitrev1.Record, admit func(ctx context.Context, rec, cur *maitrev1.Record) error) (bool, error) {
	// A tombstone's empty body arrives as nil from the wire; the column
	// holds an empty blob, never NULL.
	body := rec.GetBody()
	if body == nil {
		body = []byte{}
	}
	// Admission (decision 189) reads the store, so it runs before the
	// transaction takes the only connection; the transaction checks again
	// that the Record is still newer.
	cur, err := s.getRaw(ctx, rec.Kind, rec.Id)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return false, err
	}
	if cur != nil && !newer(rec, cur) {
		return false, nil
	}
	if admit != nil {
		if err := admit(ctx, rec, cur); err != nil {
			return false, err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var held maitrev1.Record
	err = tx.QueryRowContext(ctx, `SELECT owner_node_id, version, updated_unix_ms FROM records WHERE kind=? AND id=?`, rec.Kind, rec.Id).Scan(&held.OwnerNodeId, &held.Version, &held.UpdatedUnixMs)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if err == nil && !newer(rec, &held) {
		return false, nil
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO records (kind, id, owner_node_id, version, updated_unix_ms, body, deleted, signer_key, signature, signed_unix_ms)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(kind, id) DO UPDATE SET
  owner_node_id=excluded.owner_node_id, version=excluded.version,
  updated_unix_ms=excluded.updated_unix_ms, body=excluded.body, deleted=excluded.deleted,
  signer_key=excluded.signer_key, signature=excluded.signature, signed_unix_ms=excluded.signed_unix_ms`,
		rec.Kind, rec.Id, rec.OwnerNodeId, rec.Version, rec.UpdatedUnixMs, body, boolInt(rec.Deleted), nz(rec.SignerKey), nz(rec.Signature), rec.SignedUnixMs); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// newer is the newest-wins rule: a higher version from the same owner, or
// any version from a different owner with a later timestamp (ties broken
// by owner ID so every Node converges on the same answer).
func newer(rec, cur *maitrev1.Record) bool {
	return (cur.OwnerNodeId == rec.OwnerNodeId && rec.Version > cur.Version) ||
		(cur.OwnerNodeId != rec.OwnerNodeId && (rec.UpdatedUnixMs > cur.UpdatedUnixMs ||
			(rec.UpdatedUnixMs == cur.UpdatedUnixMs && rec.OwnerNodeId > cur.OwnerNodeId)))
}

// Raw returns the Record (kind, id), tombstone or not, with its signature.
func (s *Store) Raw(ctx context.Context, kind, id string) (*maitrev1.Record, error) {
	return s.getRaw(ctx, kind, id)
}

// Get unmarshals the live Record (kind, id) into body.
func (s *Store) Get(ctx context.Context, kind, id string, body proto.Message) (*maitrev1.Record, error) {
	rec, err := s.getRaw(ctx, kind, id)
	if err != nil {
		return nil, err
	}
	if rec.Deleted {
		return nil, ErrNotFound
	}
	if body != nil {
		if err := proto.Unmarshal(rec.Body, body); err != nil {
			return nil, err
		}
	}
	return rec, nil
}

func (s *Store) getRaw(ctx context.Context, kind, id string) (*maitrev1.Record, error) {
	row := s.db.QueryRowContext(ctx, `SELECT kind, id, owner_node_id, version, updated_unix_ms, body, deleted, signer_key, signature, signed_unix_ms FROM records WHERE kind=? AND id=?`, kind, id)
	rec, err := scan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return rec, err
}

// List returns every Record, tombstones included, of one kind or all kinds
// when kind is empty. Order is kind then id.
func (s *Store) List(ctx context.Context, kind string) ([]*maitrev1.Record, error) {
	q := `SELECT kind, id, owner_node_id, version, updated_unix_ms, body, deleted, signer_key, signature, signed_unix_ms FROM records`
	var args []any
	if kind != "" {
		q += ` WHERE kind=?`
		args = append(args, kind)
	}
	q += ` ORDER BY kind, id`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*maitrev1.Record
	for rows.Next() {
		rec, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// ListLive is List without tombstones, each body unmarshalled via newBody.
func ListLive[T proto.Message](ctx context.Context, s *Store, kind string, newBody func() T) ([]T, error) {
	recs, err := s.List(ctx, kind)
	if err != nil {
		return nil, err
	}
	var out []T
	for _, r := range recs {
		if r.Deleted {
			continue
		}
		b := newBody()
		if err := proto.Unmarshal(r.Body, b); err != nil {
			return nil, fmt.Errorf("record %s/%s: %w", r.Kind, r.Id, err)
		}
		out = append(out, b)
	}
	return out, nil
}

type scanner interface{ Scan(dest ...any) error }

func scan(row scanner) (*maitrev1.Record, error) {
	var rec maitrev1.Record
	var deleted int
	if err := row.Scan(&rec.Kind, &rec.Id, &rec.OwnerNodeId, &rec.Version, &rec.UpdatedUnixMs, &rec.Body, &deleted, &rec.SignerKey, &rec.Signature, &rec.SignedUnixMs); err != nil {
		return nil, err
	}
	rec.Deleted = deleted != 0
	return &rec, nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
