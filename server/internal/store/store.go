// Package store is the Fleet database on one Node: SQLite holding Records
// (ADR 0003). Milestone 1 ships the single-Node form; the Record shape and
// the ownership rule are final here so Milestone 3 only adds exchange.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"google.golang.org/protobuf/proto"
	_ "modernc.org/sqlite"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
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
}

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
`)
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

func (s *Store) write(ctx context.Context, kind, id string, raw []byte, deleted, fleetWide bool) (*maitrev1.Record, error) {
	if raw == nil {
		raw = []byte{}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var owner string
	var version uint64
	err = tx.QueryRowContext(ctx, `SELECT owner_node_id, version FROM records WHERE kind=? AND id=?`, kind, id).Scan(&owner, &version)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return nil, err
	case owner != s.nodeID && !fleetWide:
		return nil, fmt.Errorf("%w: %s", ErrNotOwner, owner)
	}
	rec := &maitrev1.Record{
		Kind:          kind,
		Id:            id,
		OwnerNodeId:   s.nodeID,
		Version:       version + 1,
		UpdatedUnixMs: s.now().UnixMilli(),
		Body:          raw,
		Deleted:       deleted,
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO records (kind, id, owner_node_id, version, updated_unix_ms, body, deleted)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(kind, id) DO UPDATE SET
  owner_node_id=excluded.owner_node_id, version=excluded.version,
  updated_unix_ms=excluded.updated_unix_ms, body=excluded.body, deleted=excluded.deleted`,
		rec.Kind, rec.Id, rec.OwnerNodeId, rec.Version, rec.UpdatedUnixMs, rec.Body, boolInt(rec.Deleted)); err != nil {
		return nil, err
	}
	return rec, tx.Commit()
}

// Apply stores a Record received from another Node verbatim if it is newer
// than what we hold: higher version from the same owner, or any version from
// a different owner with a later timestamp (ties broken by owner ID so every
// Node converges on the same answer). Returns true if applied.
func (s *Store) Apply(ctx context.Context, rec *maitrev1.Record) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var owner string
	var version uint64
	var updated int64
	err = tx.QueryRowContext(ctx, `SELECT owner_node_id, version, updated_unix_ms FROM records WHERE kind=? AND id=?`, rec.Kind, rec.Id).Scan(&owner, &version, &updated)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if err == nil {
		newer := (owner == rec.OwnerNodeId && rec.Version > version) ||
			(owner != rec.OwnerNodeId && (rec.UpdatedUnixMs > updated ||
				(rec.UpdatedUnixMs == updated && rec.OwnerNodeId > owner)))
		if !newer {
			return false, nil
		}
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO records (kind, id, owner_node_id, version, updated_unix_ms, body, deleted)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(kind, id) DO UPDATE SET
  owner_node_id=excluded.owner_node_id, version=excluded.version,
  updated_unix_ms=excluded.updated_unix_ms, body=excluded.body, deleted=excluded.deleted`,
		rec.Kind, rec.Id, rec.OwnerNodeId, rec.Version, rec.UpdatedUnixMs, rec.Body, boolInt(rec.Deleted)); err != nil {
		return false, err
	}
	return true, tx.Commit()
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
	row := s.db.QueryRowContext(ctx, `SELECT kind, id, owner_node_id, version, updated_unix_ms, body, deleted FROM records WHERE kind=? AND id=?`, kind, id)
	rec, err := scan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return rec, err
}

// List returns every Record, tombstones included, of one kind or all kinds
// when kind is empty. Order is kind then id.
func (s *Store) List(ctx context.Context, kind string) ([]*maitrev1.Record, error) {
	q := `SELECT kind, id, owner_node_id, version, updated_unix_ms, body, deleted FROM records`
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
	if err := row.Scan(&rec.Kind, &rec.Id, &rec.OwnerNodeId, &rec.Version, &rec.UpdatedUnixMs, &rec.Body, &deleted); err != nil {
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
