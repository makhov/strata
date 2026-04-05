package object

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
)

const (
	encVersion   = byte(0x01)
	encHeaderLen = 14   // 1 version + 1 keyID + 12 nonce
	encChunkSize = 64 * 1024
	encTagSize   = 16 // AES-GCM authentication tag
)

// KeyProvider supplies the AES-256 key used to encrypt and decrypt objects.
//
// V1 ships [StaticKeyProvider]. Future implementations may fetch from AWS KMS,
// GCP KMS, or HashiCorp Vault — the Key call accepts a context for that reason.
type KeyProvider interface {
	// Key returns the 32-byte AES-256 encryption key.
	Key(ctx context.Context) ([32]byte, error)

	// KeyID returns a 0–255 identifier stored in each encrypted object header.
	// It is reserved for future key-rotation support; V1 implementations always
	// return 0.
	KeyID() uint8
}

// StaticKeyProvider is a [KeyProvider] backed by a fixed 32-byte AES-256 key.
// It is safe for concurrent use.
type StaticKeyProvider struct {
	key [32]byte
}

// NewStaticKeyProvider returns a [KeyProvider] that always returns the given key.
// key must be exactly 32 bytes.
func NewStaticKeyProvider(key []byte) (*StaticKeyProvider, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("object: AES-256 key must be 32 bytes, got %d", len(key))
	}
	p := &StaticKeyProvider{}
	copy(p.key[:], key)
	return p, nil
}

func (p *StaticKeyProvider) Key(_ context.Context) ([32]byte, error) { return p.key, nil }
func (p *StaticKeyProvider) KeyID() uint8                            { return 0 }

// EncryptedStore wraps a [Store] and transparently encrypts all object data
// with AES-256-GCM using chunked streaming (64 KiB chunks). Delete and List
// pass through unchanged; keys are always stored in plaintext.
//
// If the inner store also implements [ConditionalStore] or [VersionedStore],
// EncryptedStore propagates those interfaces, encrypting/decrypting the object
// body while delegating the conditional logic to the inner store.
type EncryptedStore struct {
	inner Store
	kp    KeyProvider
}

// NewEncryptedStore returns a [Store] that encrypts all Put/Get data using kp.
func NewEncryptedStore(inner Store, kp KeyProvider) *EncryptedStore {
	return &EncryptedStore{inner: inner, kp: kp}
}

// Inner returns the wrapped store.
func (e *EncryptedStore) Inner() Store { return e.inner }

func (e *EncryptedStore) Put(ctx context.Context, key string, r io.Reader) error {
	k, err := e.kp.Key(ctx)
	if err != nil {
		return fmt.Errorf("object: encrypt key: %w", err)
	}
	pr, pw := io.Pipe()
	go func() { pw.CloseWithError(encryptStream(pw, r, k, e.kp.KeyID())) }()
	return e.inner.Put(ctx, key, pr)
}

func (e *EncryptedStore) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	rc, err := e.inner.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	k, err := e.kp.Key(ctx)
	if err != nil {
		rc.Close()
		return nil, fmt.Errorf("object: decrypt key: %w", err)
	}
	return newDecryptReader(rc, k)
}

func (e *EncryptedStore) Delete(ctx context.Context, key string) error {
	return e.inner.Delete(ctx, key)
}

func (e *EncryptedStore) List(ctx context.Context, prefix string) ([]string, error) {
	return e.inner.List(ctx, prefix)
}

// GetETag implements [ConditionalStore]. The ETag is computed by the inner
// store over the ciphertext — it is opaque for CAS purposes and unaffected by
// encryption. The returned body is transparently decrypted.
func (e *EncryptedStore) GetETag(ctx context.Context, key string) (*GetWithETag, error) {
	cs, ok := e.inner.(ConditionalStore)
	if !ok {
		return nil, fmt.Errorf("object: inner store does not implement ConditionalStore")
	}
	r, err := cs.GetETag(ctx, key)
	if err != nil {
		return nil, err
	}
	k, err := e.kp.Key(ctx)
	if err != nil {
		r.Body.Close()
		return nil, fmt.Errorf("object: decrypt key: %w", err)
	}
	dec, err := newDecryptReader(r.Body, k)
	if err != nil {
		return nil, err
	}
	return &GetWithETag{Body: dec, ETag: r.ETag}, nil
}

// PutIfAbsent implements [ConditionalStore].
func (e *EncryptedStore) PutIfAbsent(ctx context.Context, key string, r io.Reader) error {
	cs, ok := e.inner.(ConditionalStore)
	if !ok {
		return fmt.Errorf("object: inner store does not implement ConditionalStore")
	}
	k, err := e.kp.Key(ctx)
	if err != nil {
		return fmt.Errorf("object: encrypt key: %w", err)
	}
	pr, pw := io.Pipe()
	go func() { pw.CloseWithError(encryptStream(pw, r, k, e.kp.KeyID())) }()
	return cs.PutIfAbsent(ctx, key, pr)
}

// PutIfMatch implements [ConditionalStore].
func (e *EncryptedStore) PutIfMatch(ctx context.Context, key string, r io.Reader, matchETag string) error {
	cs, ok := e.inner.(ConditionalStore)
	if !ok {
		return fmt.Errorf("object: inner store does not implement ConditionalStore")
	}
	k, err := e.kp.Key(ctx)
	if err != nil {
		return fmt.Errorf("object: encrypt key: %w", err)
	}
	pr, pw := io.Pipe()
	go func() { pw.CloseWithError(encryptStream(pw, r, k, e.kp.KeyID())) }()
	return cs.PutIfMatch(ctx, key, pr, matchETag)
}

// GetVersioned implements [VersionedStore].
func (e *EncryptedStore) GetVersioned(ctx context.Context, key, versionID string) (io.ReadCloser, error) {
	vs, ok := e.inner.(VersionedStore)
	if !ok {
		return nil, fmt.Errorf("object: inner store does not implement VersionedStore")
	}
	rc, err := vs.GetVersioned(ctx, key, versionID)
	if err != nil {
		return nil, err
	}
	k, err := e.kp.Key(ctx)
	if err != nil {
		rc.Close()
		return nil, fmt.Errorf("object: decrypt key: %w", err)
	}
	return newDecryptReader(rc, k)
}

// ── Streaming encryption ──────────────────────────────────────────────────────

// encryptStream writes the AES-256-GCM encrypted form of r to w.
//
// Wire format:
//
//	[1: version=0x01][1: keyID][12: fileNonce]
//	repeated: [4: chunkPlainLen uint32 BE][chunkPlainLen+16: ciphertext+GCM tag]
//	end:      [4: 0x00000000]
func encryptStream(w io.Writer, r io.Reader, key [32]byte, keyID uint8) error {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}

	var fileNonce [12]byte
	if _, err := rand.Read(fileNonce[:]); err != nil {
		return fmt.Errorf("object: generate nonce: %w", err)
	}

	// Header: version, keyID, 12-byte file nonce.
	var hdr [encHeaderLen]byte
	hdr[0] = encVersion
	hdr[1] = keyID
	copy(hdr[2:], fileNonce[:])
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}

	plain := make([]byte, encChunkSize)
	var sealed []byte // reused ciphertext buffer
	var idx uint64

	for {
		n, readErr := io.ReadFull(r, plain)
		if n > 0 {
			cn := chunkNonce(fileNonce, idx)
			idx++
			sealed = gcm.Seal(sealed[:0], cn[:], plain[:n], nil)

			var lbuf [4]byte
			binary.BigEndian.PutUint32(lbuf[:], uint32(n))
			if _, err := w.Write(lbuf[:]); err != nil {
				return err
			}
			if _, err := w.Write(sealed); err != nil {
				return err
			}
		}
		if readErr == io.ErrUnexpectedEOF || readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}

	// End-of-stream marker.
	_, err = w.Write([]byte{0, 0, 0, 0})
	return err
}

// chunkNonce derives a per-chunk nonce. The first 4 bytes are a random salt
// from the file nonce; the last 8 bytes are the file-nonce counter XOR'd with
// the chunk index, keeping each chunk's nonce unique within the object.
func chunkNonce(base [12]byte, i uint64) [12]byte {
	var n [12]byte
	copy(n[:4], base[:4])
	binary.BigEndian.PutUint64(n[4:], binary.BigEndian.Uint64(base[4:])^i)
	return n
}

// ── Streaming decryption ──────────────────────────────────────────────────────

type decryptReader struct {
	inner    io.Closer
	r        io.Reader
	gcm      cipher.AEAD
	baseNonce [12]byte
	chunkIdx uint64
	buf      []byte // current decrypted plaintext chunk
	pos      int    // read offset in buf
}

func newDecryptReader(rc io.ReadCloser, key [32]byte) (*decryptReader, error) {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		rc.Close()
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		rc.Close()
		return nil, err
	}
	dr := &decryptReader{inner: rc, r: rc, gcm: gcm}

	var hdr [encHeaderLen]byte
	if _, err := io.ReadFull(rc, hdr[:]); err != nil {
		rc.Close()
		return nil, fmt.Errorf("object: read encrypted header: %w", err)
	}
	if hdr[0] != encVersion {
		rc.Close()
		return nil, fmt.Errorf("object: unsupported encryption version %d", hdr[0])
	}
	// hdr[1] is keyID — reserved for future rotation, ignored in V1.
	copy(dr.baseNonce[:], hdr[2:encHeaderLen])
	return dr, nil
}

func (d *decryptReader) Read(p []byte) (int, error) {
	for {
		if d.pos < len(d.buf) {
			n := copy(p, d.buf[d.pos:])
			d.pos += n
			return n, nil
		}

		var lbuf [4]byte
		if _, err := io.ReadFull(d.r, lbuf[:]); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return 0, io.ErrUnexpectedEOF
			}
			return 0, fmt.Errorf("object: read chunk length: %w", err)
		}
		plainLen := binary.BigEndian.Uint32(lbuf[:])
		if plainLen == 0 {
			return 0, io.EOF
		}

		cipherBuf := make([]byte, int(plainLen)+encTagSize)
		if _, err := io.ReadFull(d.r, cipherBuf); err != nil {
			return 0, fmt.Errorf("object: read chunk ciphertext: %w", err)
		}

		cn := chunkNonce(d.baseNonce, d.chunkIdx)
		d.chunkIdx++
		plain, err := d.gcm.Open(cipherBuf[:0], cn[:], cipherBuf, nil)
		if err != nil {
			return 0, fmt.Errorf("object: decrypt chunk %d: authentication failed", d.chunkIdx-1)
		}
		d.buf = plain
		d.pos = 0
	}
}

func (d *decryptReader) Close() error { return d.inner.Close() }
