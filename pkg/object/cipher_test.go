package object

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

// ── StaticKeyProvider ─────────────────────────────────────────────────────────

func TestNewStaticKeyProvider_BadLength(t *testing.T) {
	_, err := NewStaticKeyProvider(make([]byte, 16))
	if err == nil {
		t.Fatal("expected error for 16-byte key")
	}
}

func TestNewStaticKeyProvider_Good(t *testing.T) {
	key := make([]byte, 32)
	key[0] = 0xAB
	kp, err := NewStaticKeyProvider(key)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, err := kp.Key(context.Background())
	if err != nil {
		t.Fatalf("Key() error: %v", err)
	}
	if got[0] != 0xAB {
		t.Fatalf("key mismatch: got[0]=%x want 0xAB", got[0])
	}
	if kp.KeyID() != 0 {
		t.Fatalf("KeyID should be 0, got %d", kp.KeyID())
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

func newTestProvider(t *testing.T) *StaticKeyProvider {
	t.Helper()
	kp, err := NewStaticKeyProvider(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	return kp
}

func newEncryptedMem(t *testing.T) (*EncryptedStore, *Mem) {
	t.Helper()
	mem := NewMem()
	es := NewEncryptedStore(mem, newTestProvider(t))
	return es, mem
}

// ── Put / Get roundtrip ───────────────────────────────────────────────────────

func TestEncryptedStore_Roundtrip(t *testing.T) {
	es, _ := newEncryptedMem(t)
	ctx := context.Background()
	plain := []byte("hello, encrypted world!")

	if err := es.Put(ctx, "k", bytes.NewReader(plain)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	rc, err := es.Get(ctx, "k")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer rc.Close()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("roundtrip mismatch: got %q want %q", got, plain)
	}
}

func TestEncryptedStore_CiphertextDiffersFromPlaintext(t *testing.T) {
	es, mem := newEncryptedMem(t)
	ctx := context.Background()
	plain := []byte("secret data that must not appear in S3")

	if err := es.Put(ctx, "key", bytes.NewReader(plain)); err != nil {
		t.Fatal(err)
	}

	// Read raw bytes from inner store (the ciphertext).
	rc, _ := mem.Get(ctx, "key")
	raw, _ := io.ReadAll(rc)
	rc.Close()

	if bytes.Contains(raw, plain) {
		t.Fatal("plaintext found verbatim in stored ciphertext")
	}
}

func TestEncryptedStore_EmptyValue(t *testing.T) {
	es, _ := newEncryptedMem(t)
	ctx := context.Background()

	if err := es.Put(ctx, "empty", bytes.NewReader(nil)); err != nil {
		t.Fatalf("Put empty: %v", err)
	}
	rc, err := es.Get(ctx, "empty")
	if err != nil {
		t.Fatalf("Get empty: %v", err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if len(got) != 0 {
		t.Fatalf("expected empty, got %d bytes", len(got))
	}
}

func TestEncryptedStore_WrongKey(t *testing.T) {
	mem := NewMem()
	ctx := context.Background()

	kp1, _ := NewStaticKeyProvider(bytes.Repeat([]byte{0x01}, 32))
	kp2, _ := NewStaticKeyProvider(bytes.Repeat([]byte{0x02}, 32))

	if err := NewEncryptedStore(mem, kp1).Put(ctx, "k", strings.NewReader("data")); err != nil {
		t.Fatal(err)
	}
	rc, err := NewEncryptedStore(mem, kp2).Get(ctx, "k")
	if err != nil {
		// Some decryption errors surface in newDecryptReader (header), others in Read.
		return
	}
	defer rc.Close()
	_, err = io.ReadAll(rc)
	if err == nil {
		t.Fatal("expected error when decrypting with wrong key")
	}
}

func TestEncryptedStore_TruncatedCiphertext(t *testing.T) {
	es, mem := newEncryptedMem(t)
	ctx := context.Background()

	if err := es.Put(ctx, "k", strings.NewReader("some data")); err != nil {
		t.Fatal(err)
	}

	// Truncate the stored ciphertext.
	rc, _ := mem.Get(ctx, "k")
	raw, _ := io.ReadAll(rc)
	rc.Close()
	_ = mem.Put(ctx, "k", bytes.NewReader(raw[:len(raw)/2]))

	r, err := es.Get(ctx, "k")
	if err != nil {
		return // header read failure is acceptable
	}
	defer r.Close()
	_, err = io.ReadAll(r)
	if err == nil {
		t.Fatal("expected error reading truncated ciphertext")
	}
}

// ── Chunk boundary tests ──────────────────────────────────────────────────────

func TestEncryptedStore_ExactlyOneChunk(t *testing.T) {
	testRoundtrip(t, encChunkSize)
}

func TestEncryptedStore_OneChunkPlusOne(t *testing.T) {
	testRoundtrip(t, encChunkSize+1)
}

func TestEncryptedStore_MultipleChunks(t *testing.T) {
	testRoundtrip(t, 5*encChunkSize+7)
}

func testRoundtrip(t *testing.T, size int) {
	t.Helper()
	es, _ := newEncryptedMem(t)
	ctx := context.Background()

	plain := make([]byte, size)
	for i := range plain {
		plain[i] = byte(i)
	}

	if err := es.Put(ctx, "k", bytes.NewReader(plain)); err != nil {
		t.Fatalf("Put(%d bytes): %v", size, err)
	}
	rc, err := es.Get(ctx, "k")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer rc.Close()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("roundtrip mismatch for %d bytes", size)
	}
}

// ── Different random nonce per object ─────────────────────────────────────────

func TestEncryptedStore_TwoIdenticalPlaintextsProduceDifferentCiphertexts(t *testing.T) {
	es, mem := newEncryptedMem(t)
	ctx := context.Background()
	plain := []byte("same content")

	_ = es.Put(ctx, "a", bytes.NewReader(plain))
	_ = es.Put(ctx, "b", bytes.NewReader(plain))

	rca, _ := mem.Get(ctx, "a")
	rawA, _ := io.ReadAll(rca)
	rca.Close()

	rcb, _ := mem.Get(ctx, "b")
	rawB, _ := io.ReadAll(rcb)
	rcb.Close()

	if bytes.Equal(rawA, rawB) {
		t.Fatal("two puts of the same plaintext produced identical ciphertext (nonce reuse?)")
	}
}

// ── Delete / List pass-through ────────────────────────────────────────────────

func TestEncryptedStore_DeleteList(t *testing.T) {
	es, _ := newEncryptedMem(t)
	ctx := context.Background()

	_ = es.Put(ctx, "a", strings.NewReader("x"))
	_ = es.Put(ctx, "b", strings.NewReader("y"))

	keys, err := es.List(ctx, "")
	if err != nil || len(keys) != 2 {
		t.Fatalf("List: got %v err=%v", keys, err)
	}

	if err := es.Delete(ctx, "a"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	keys, _ = es.List(ctx, "")
	if len(keys) != 1 || keys[0] != "b" {
		t.Fatalf("after Delete, expected [b], got %v", keys)
	}
}

// ── ConditionalStore delegation ───────────────────────────────────────────────

func TestEncryptedStore_ConditionalStore(t *testing.T) {
	es, _ := newEncryptedMem(t)
	ctx := context.Background()

	// PutIfAbsent should succeed on a new key.
	if err := es.PutIfAbsent(ctx, "k", strings.NewReader("v1")); err != nil {
		t.Fatalf("PutIfAbsent: %v", err)
	}
	// PutIfAbsent should fail because key now exists.
	err := es.PutIfAbsent(ctx, "k", strings.NewReader("v2"))
	if !errors.Is(err, ErrPreconditionFailed) {
		t.Fatalf("expected ErrPreconditionFailed, got %v", err)
	}

	// GetETag returns decrypted body and an ETag.
	r, err := es.GetETag(ctx, "k")
	if err != nil {
		t.Fatalf("GetETag: %v", err)
	}
	body, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if string(body) != "v1" {
		t.Fatalf("GetETag body: got %q want v1", body)
	}
	if r.ETag == "" {
		t.Fatal("GetETag returned empty ETag")
	}

	// PutIfMatch with correct ETag should succeed.
	if err := es.PutIfMatch(ctx, "k", strings.NewReader("v3"), r.ETag); err != nil {
		t.Fatalf("PutIfMatch with correct ETag: %v", err)
	}

	// PutIfMatch with stale ETag should fail.
	err = es.PutIfMatch(ctx, "k", strings.NewReader("v4"), r.ETag)
	if !errors.Is(err, ErrPreconditionFailed) {
		t.Fatalf("PutIfMatch stale: expected ErrPreconditionFailed, got %v", err)
	}

	// Final value should be "v3".
	rc, _ := es.Get(ctx, "k")
	final, _ := io.ReadAll(rc)
	rc.Close()
	if string(final) != "v3" {
		t.Fatalf("final value: got %q want v3", final)
	}
}

func TestEncryptedStore_ConditionalStore_NotSupported(t *testing.T) {
	// A Store that does NOT implement ConditionalStore.
	es := NewEncryptedStore(&noopStore{}, newTestProvider(t))
	ctx := context.Background()

	if _, err := es.GetETag(ctx, "k"); err == nil {
		t.Fatal("expected error when inner has no ConditionalStore")
	}
	if err := es.PutIfAbsent(ctx, "k", strings.NewReader("v")); err == nil {
		t.Fatal("expected error when inner has no ConditionalStore")
	}
	if err := es.PutIfMatch(ctx, "k", strings.NewReader("v"), "etag"); err == nil {
		t.Fatal("expected error when inner has no ConditionalStore")
	}
}

func TestEncryptedStore_VersionedStore_NotSupported(t *testing.T) {
	es := NewEncryptedStore(&noopStore{}, newTestProvider(t))
	ctx := context.Background()

	if _, err := es.GetVersioned(ctx, "k", "v1"); err == nil {
		t.Fatal("expected error when inner has no VersionedStore")
	}
}

// noopStore is a minimal Store that does not implement any optional interfaces.
type noopStore struct{}

func (s *noopStore) Put(_ context.Context, _ string, _ io.Reader) error      { return nil }
func (s *noopStore) Get(_ context.Context, _ string) (io.ReadCloser, error)  { return nil, ErrNotFound }
func (s *noopStore) Delete(_ context.Context, _ string) error                { return nil }
func (s *noopStore) List(_ context.Context, _ string) ([]string, error)      { return nil, nil }
