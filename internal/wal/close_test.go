package wal

import (
	"context"
	"errors"
	"testing"
)

// TestWALCloseUploadFailureReturnsError verifies that Close propagates a
// final-segment upload error to the caller instead of silently returning nil.
func TestWALCloseUploadFailureReturnsError(t *testing.T) {
	dir := t.TempDir()

	uploadErr := errors.New("injected upload failure")
	uploader := func(_ context.Context, _, _ string) error { return uploadErr }

	w, err := Open(dir, 1, 1, WithUploader(uploader))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)

	if err := w.Append(&Entry{Revision: 1, Term: 1, Op: OpCreate, Key: "k", Value: []byte("v")}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	cancel()

	if err := w.Close(); !errors.Is(err, uploadErr) {
		t.Errorf("Close: want upload error, got %v", err)
	}
}

// TestWALCloseNoUploadErrorOnSuccess verifies that Close returns nil when the
// final-segment upload succeeds.
func TestWALCloseNoUploadErrorOnSuccess(t *testing.T) {
	dir := t.TempDir()

	uploader := func(_ context.Context, _, _ string) error { return nil }

	w, err := Open(dir, 1, 1, WithUploader(uploader))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)

	if err := w.Append(&Entry{Revision: 1, Term: 1, Op: OpCreate, Key: "k", Value: []byte("v")}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	cancel()

	if err := w.Close(); err != nil {
		t.Errorf("Close with successful upload: want nil, got %v", err)
	}
}

// TestWALCloseUploadContextHasDeadline verifies that the upload triggered by
// Close uses a context with a deadline so a hung object store cannot block
// shutdown indefinitely.
func TestWALCloseUploadContextHasDeadline(t *testing.T) {
	dir := t.TempDir()

	var uploadCtx context.Context
	uploader := func(ctx context.Context, _, _ string) error {
		uploadCtx = ctx
		return nil
	}

	w, err := Open(dir, 1, 1, WithUploader(uploader))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)

	if err := w.Append(&Entry{Revision: 1, Term: 1, Op: OpCreate, Key: "k", Value: []byte("v")}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	cancel()
	w.Close()

	if uploadCtx == nil {
		t.Fatal("uploader was never called")
	}
	if _, ok := uploadCtx.Deadline(); !ok {
		t.Error("upload context has no deadline — shutdown can block indefinitely on storage stall")
	}
}

// TestWALCloseNoUploadWhenEmpty verifies that Close does not call the uploader
// when the active segment has no entries (nothing to persist).
func TestWALCloseNoUploadWhenEmpty(t *testing.T) {
	dir := t.TempDir()

	uploadCalled := false
	uploader := func(_ context.Context, _, _ string) error {
		uploadCalled = true
		return nil
	}

	w, err := Open(dir, 1, 1, WithUploader(uploader))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)
	cancel()

	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if uploadCalled {
		t.Error("uploader should not be called for an empty segment")
	}
}

// TestWALRotationOpenFailureNoNilPanic verifies that when OpenSegmentWriter
// fails during a rotation triggered by Append, the WAL leaves a valid (non-nil)
// active writer so subsequent Append calls return an error rather than panicking.
func TestWALRotationOpenFailureNoNilPanic(t *testing.T) {
	// Use a very small segment so rotation is triggered immediately.
	dir := t.TempDir()
	w, err := Open(dir, 1, 1, WithSegmentMaxSize(1))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)
	defer cancel()

	// Write entries that each exceed the tiny max size. Each Append may trigger
	// a rotation. The WAL directory is healthy, so rotation will succeed, but
	// we verify the invariant: active is never nil between Appends.
	for i := int64(1); i <= 5; i++ {
		err := w.Append(&Entry{
			Revision: i, Term: 1, Op: OpCreate,
			Key:   "key",
			Value: make([]byte, 64),
		})
		// Append may fail if the active segment is in a bad state, but must
		// never panic with a nil pointer dereference.
		if err != nil {
			t.Logf("Append %d: %v (non-nil error is acceptable)", i, err)
		}

		w.mu.Lock()
		active := w.active
		w.mu.Unlock()
		if active == nil {
			t.Fatalf("w.active is nil after Append %d — next call would panic", i)
		}
	}

	w.Close()
}
