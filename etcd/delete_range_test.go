package etcd_test

import (
	"context"
	"testing"

	"go.etcd.io/etcd/api/v3/etcdserverpb"
)

// TestDeleteRangeDeletedCountMissingKey is the regression test for the P2 fix:
// DeleteRange must report Deleted=0 when the key does not exist, rather than
// always returning 1.
func TestDeleteRangeDeletedCountMissingKey(t *testing.T) {
	srv := newServer(t)
	ctx := context.Background()

	resp, err := srv.DeleteRange(ctx, &etcdserverpb.DeleteRangeRequest{
		Key: []byte("/no/such/key"),
	})
	if err != nil {
		t.Fatalf("DeleteRange: %v", err)
	}
	if resp.Deleted != 0 {
		t.Errorf("Deleted: want 0 for missing key, got %d", resp.Deleted)
	}
}

// TestDeleteRangeDeletedCountExistingKey verifies the positive case: Deleted=1
// when the key exists and is successfully removed.
func TestDeleteRangeDeletedCountExistingKey(t *testing.T) {
	srv := newServer(t)
	ctx := context.Background()

	put(t, srv, "/del/key", "val")

	resp, err := srv.DeleteRange(ctx, &etcdserverpb.DeleteRangeRequest{
		Key: []byte("/del/key"),
	})
	if err != nil {
		t.Fatalf("DeleteRange: %v", err)
	}
	if resp.Deleted != 1 {
		t.Errorf("Deleted: want 1 for existing key, got %d", resp.Deleted)
	}
}

// TestDeleteRangeDeletedCountIdempotent verifies that deleting the same key
// twice returns Deleted=1 on the first call and Deleted=0 on the second.
func TestDeleteRangeDeletedCountIdempotent(t *testing.T) {
	srv := newServer(t)
	ctx := context.Background()

	put(t, srv, "/del/idempotent", "val")

	resp, err := srv.DeleteRange(ctx, &etcdserverpb.DeleteRangeRequest{
		Key: []byte("/del/idempotent"),
	})
	if err != nil || resp.Deleted != 1 {
		t.Fatalf("first DeleteRange: err=%v deleted=%d", err, resp.Deleted)
	}

	resp, err = srv.DeleteRange(ctx, &etcdserverpb.DeleteRangeRequest{
		Key: []byte("/del/idempotent"),
	})
	if err != nil {
		t.Fatalf("second DeleteRange: %v", err)
	}
	if resp.Deleted != 0 {
		t.Errorf("second DeleteRange: want Deleted=0, got %d", resp.Deleted)
	}
}
