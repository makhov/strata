package t4doc

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/t4db/t4"
	"github.com/t4db/t4/pkg/object"
)

func newTestCollection(t *testing.T) (*t4.Node, *DB, *Collection[map[string]any], context.Context) {
	t.Helper()
	node, err := t4.Open(t4.Config{DataDir: t.TempDir(), ObjectStore: object.NewMem()})
	if err != nil {
		t.Fatalf("open node: %v", err)
	}
	t.Cleanup(func() { node.Close() })
	db := Open(node)
	t.Cleanup(db.Close)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)
	return node, db, NewCollection[map[string]any](db, "users"), ctx
}

func TestCollectionPutGetPatchDelete(t *testing.T) {
	_, _, users, ctx := newTestCollection(t)

	if _, err := users.Insert(ctx, "alice", map[string]any{"name": "Alice", "status": "new"}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if _, err := users.Insert(ctx, "alice", map[string]any{"name": "Alice"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("insert existing: got %v, want ErrConflict", err)
	}
	if _, err := users.Patch(ctx, "alice", MergePatch(`{"status":"active","role":"admin"}`)); err != nil {
		t.Fatalf("patch: %v", err)
	}
	doc, err := users.FindByID(ctx, "alice")
	if err != nil {
		t.Fatalf("find by id: %v", err)
	}
	if doc.Value["status"] != "active" || doc.Value["role"] != "admin" {
		t.Fatalf("patched document = %#v", doc.Value)
	}
	if _, err := users.Delete(ctx, "alice"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := users.FindByID(ctx, "alice"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("find deleted: got %v, want ErrNotFound", err)
	}
}

func TestFindUsesBackfilledEqualityIndex(t *testing.T) {
	_, _, users, ctx := newTestCollection(t)

	_, _ = users.Put(ctx, "alice", map[string]any{"email": "alice@example.com", "status": "active", "age": 34})
	_, _ = users.Put(ctx, "bob", map[string]any{"email": "bob@example.com", "status": "inactive", "age": 41})
	if err := users.CreateIndex(ctx, IndexSpec{Name: "email", Field: "email", Type: String}); err != nil {
		t.Fatalf("create index: %v", err)
	}

	docs, err := users.Find(ctx, Eq("email", "alice@example.com"))
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if len(docs) != 1 || docs[0].ID != "alice" {
		t.Fatalf("docs = %#v", docs)
	}
}

func TestFindMissingIndexAndBoundedScan(t *testing.T) {
	_, _, users, ctx := newTestCollection(t)
	for _, id := range []string{"a", "b", "c"} {
		_, _ = users.Put(ctx, id, map[string]any{"status": "active"})
	}

	if _, err := users.Find(ctx, Eq("status", "active")); !errors.Is(err, ErrMissingIndex) {
		t.Fatalf("find without index: got %v, want ErrMissingIndex", err)
	}
	docs, err := users.Find(ctx, Eq("status", "active"), AllowScan(), MaxScan(10), Limit(2))
	if err != nil {
		t.Fatalf("allow scan: %v", err)
	}
	if len(docs) != 2 {
		t.Fatalf("got %d docs, want 2", len(docs))
	}
	docs, err = users.Find(ctx, Eq("status", "missing"), AllowScan(), MaxScan(3))
	if err != nil {
		t.Fatalf("scan at exact max: %v", err)
	}
	if len(docs) != 0 {
		t.Fatalf("scan at exact max returned %d docs, want 0", len(docs))
	}
	if _, err := users.Find(ctx, Eq("status", "missing"), AllowScan(), MaxScan(2)); !errors.Is(err, ErrScanLimitExceeded) {
		t.Fatalf("bounded scan: got %v, want ErrScanLimitExceeded", err)
	}
}

func TestFindSkipsStaleIndexEntries(t *testing.T) {
	node, _, users, ctx := newTestCollection(t)
	if err := users.CreateIndex(ctx, IndexSpec{Name: "status", Field: "status", Type: String}); err != nil {
		t.Fatalf("create index: %v", err)
	}
	spec := IndexSpec{Name: "status", Field: "status", Type: String, State: IndexReady}
	encoded, ok := encodeIndexValue(spec, "active")
	if !ok {
		t.Fatal("encode index value failed")
	}
	if _, err := node.Put(ctx, indexKey("users", "status", encoded, "ghost"), []byte("1"), 0); err != nil {
		t.Fatalf("put stale index: %v", err)
	}
	docs, err := users.Find(ctx, Eq("status", "active"))
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if len(docs) != 0 {
		t.Fatalf("got stale docs: %#v", docs)
	}
}

func TestWatchHubFanout(t *testing.T) {
	_, _, users, ctx := newTestCollection(t)
	active, err := users.Watch(ctx, Eq("status", "active"))
	if err != nil {
		t.Fatalf("watch active: %v", err)
	}
	inactive, err := users.Watch(ctx, Eq("status", "inactive"))
	if err != nil {
		t.Fatalf("watch inactive: %v", err)
	}

	if _, err := users.Put(ctx, "alice", map[string]any{"status": "active"}); err != nil {
		t.Fatalf("put active: %v", err)
	}
	if _, err := users.Put(ctx, "bob", map[string]any{"status": "inactive"}); err != nil {
		t.Fatalf("put inactive: %v", err)
	}

	gotActive := recvChange(t, active)
	if gotActive.Document.ID != "alice" {
		t.Fatalf("active watcher got %q", gotActive.Document.ID)
	}
	gotInactive := recvChange(t, inactive)
	if gotInactive.Document.ID != "bob" {
		t.Fatalf("inactive watcher got %q", gotInactive.Document.ID)
	}
}

func TestInvalidNames(t *testing.T) {
	_, db, users, ctx := newTestCollection(t)
	if _, err := users.Put(ctx, "", map[string]any{}); !errors.Is(err, ErrInvalidDocumentID) {
		t.Fatalf("empty id: got %v, want ErrInvalidDocumentID", err)
	}
	if _, err := users.Put(ctx, string([]byte{0xff}), map[string]any{}); !errors.Is(err, ErrInvalidDocumentID) {
		t.Fatalf("invalid utf8 id: got %v, want ErrInvalidDocumentID", err)
	}
	bad := NewCollection[map[string]any](db, "bad/name")
	if _, err := bad.Put(ctx, "id", map[string]any{}); !errors.Is(err, ErrInvalidName) {
		t.Fatalf("bad collection: got %v, want ErrInvalidName", err)
	}
}

func recvChange(t *testing.T, ch <-chan Change[map[string]any]) Change[map[string]any] {
	t.Helper()
	select {
	case ev := <-ch:
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for watch event")
	}
	return Change[map[string]any]{}
}
