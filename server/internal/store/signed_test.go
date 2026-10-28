package store_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/identity"
	"github.com/Onfire7/maitre-de-serveur/server/internal/recsig"
	"github.com/Onfire7/maitre-de-serveur/server/internal/store"
)

// Decision 189: a database from before signing gains the columns and keeps
// its rows; the Node signs its own as new versions.
func TestUpgradeAndResign(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fleet.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
CREATE TABLE records (kind TEXT NOT NULL, id TEXT NOT NULL, owner_node_id TEXT NOT NULL, version INTEGER NOT NULL,
  updated_unix_ms INTEGER NOT NULL, body BLOB NOT NULL, deleted INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (kind, id)) WITHOUT ROWID;
CREATE TABLE outbox (seq INTEGER PRIMARY KEY AUTOINCREMENT, owner_node_id TEXT NOT NULL, kind TEXT NOT NULL, id TEXT NOT NULL,
  body BLOB NOT NULL, deleted INTEGER NOT NULL DEFAULT 0, created_unix_ms INTEGER NOT NULL);
INSERT INTO records VALUES ('service', 'web', 'me', 3, 1, x'0a03776562', 0);
INSERT INTO records VALUES ('space', 'media', 'me', 1, 1, x'', 0);
INSERT INTO records VALUES ('drive', 'd', 'other', 1, 1, x'', 0);`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	id, _ := identity.Generate()
	s, err := store.Open(path, "me")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.SignWith(id)
	ctx := context.Background()
	recs, err := s.ResignOwn(ctx, func(kind string) bool { return kind != "space" })
	if err != nil || len(recs) != 1 || recs[0].Kind != "service" || recs[0].Version != 4 {
		t.Fatalf("resigned %v, %v", recs, err)
	}
	if fp, err := recsig.Verify(recs[0]); err != nil || fp != id.Fingerprint() {
		t.Fatalf("verify: %q %v", fp, err)
	}
	if r, _ := s.Raw(ctx, "space", "media"); len(r.Signature) != 0 {
		t.Fatal("a Device's kind was signed by the Node")
	}
	if r, _ := s.Raw(ctx, "drive", "d"); len(r.Signature) != 0 {
		t.Fatal("another Node's record was signed")
	}
}

func TestPutSignedAndStale(t *testing.T) {
	s := open(t, "me")
	dev, _ := identity.Generate()
	ctx := context.Background()
	rec := &maitrev1.Record{Kind: "space", Id: "media", Body: []byte("v1")}
	if err := recsig.Sign(dev, rec, 100); err != nil {
		t.Fatal(err)
	}
	got, err := s.PutSigned(ctx, rec, true)
	if err != nil || got.OwnerNodeId != "me" || got.Version != 1 {
		t.Fatalf("put: %v %v", got, err)
	}
	if fp, err := recsig.Verify(got); err != nil || fp != dev.Fingerprint() {
		t.Fatalf("stored signature: %q %v", fp, err)
	}
	old := &maitrev1.Record{Kind: "space", Id: "media", Body: []byte("v0")}
	_ = recsig.Sign(dev, old, 100)
	if _, err := s.PutSigned(ctx, old, true); !errors.Is(err, store.ErrStale) {
		t.Fatalf("stale: %v", err)
	}
	// A Node's own writes are signed later than what they replace, even
	// when its clock is behind.
	me, _ := identity.Generate()
	s.SignWith(me)
	own, err := s.Put(ctx, "space", "media", &maitrev1.Space{Name: "x"}, true)
	if err != nil || own.SignedUnixMs <= 100 {
		t.Fatalf("own write: %v %v", own, err)
	}
	if fp, err := recsig.Verify(own); err != nil || fp != me.Fingerprint() {
		t.Fatalf("own signature: %q %v", fp, err)
	}
}

func TestAdmitAndQueue(t *testing.T) {
	s := open(t, "me")
	ctx := context.Background()
	refused := errors.New("no")
	s.Admit = func(_ context.Context, rec, cur *maitrev1.Record) error {
		if rec.Id == "bad" {
			return refused
		}
		return nil
	}
	if _, err := s.Apply(ctx, &maitrev1.Record{Kind: "k", Id: "bad", OwnerNodeId: "x", Version: 1}); !errors.Is(err, refused) {
		t.Fatalf("refused: %v", err)
	}
	if _, err := s.Raw(ctx, "k", "bad"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("a refused record was stored")
	}
	if ok, err := s.Apply(ctx, &maitrev1.Record{Kind: "k", Id: "good", OwnerNodeId: "x", Version: 1}); !ok || err != nil {
		t.Fatalf("good: %v %v", ok, err)
	}
	dev, _ := identity.Generate()
	q := &maitrev1.Record{Kind: "space", Id: "m", Body: []byte("b")}
	_ = recsig.Sign(dev, q, 7)
	if err := s.Enqueue(ctx, "owner", q); err != nil {
		t.Fatal(err)
	}
	p, err := s.Pending(ctx, "owner")
	if err != nil || len(p) != 1 {
		t.Fatalf("pending: %v %v", p, err)
	}
	if _, err := recsig.Verify(p[0].Record); err != nil {
		t.Fatalf("queued signature: %v", err)
	}
}
