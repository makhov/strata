// Package store implements the Pebble-backed key-value state machine.
//
// Pebble is used as an ordered, persistent (or in-memory) KV store. The WAL
// is the source of truth for durability; Pebble is the queryable index.
package store

import (
	"encoding/binary"
	"fmt"
)

// Key space layout (single-byte prefix preserves lexicographic order):
//
//	'c' + key bytes   → rev(8B BE) + serialised record  (current value per live key)
//	'l' + rev(8B BE)  → serialised record               (append-only change log)
//	'm' + name bytes  → metadata value                  (compact rev, current rev, etc.)
//
// The 'c' (current) space is the primary read index: a single lookup returns
// the full KeyValue for any live key. The 'l' (log) space is append-only and
// used exclusively for watch scans and compaction.
const (
	prefixCur  = byte('c')
	prefixLog  = byte('l')
	prefixMeta = byte('m')
)

var (
	metaCompactKey = []byte{prefixMeta, 'c', 'o', 'm', 'p', 'a', 'c', 't'}
	// metaCurrentRevKey persists the highest revision committed to this store.
	// Written in every Apply/Recover batch so loadMeta recovers the exact
	// current revision even when the last WAL entry was an OpCompact (which
	// does not write a log key).
	metaCurrentRevKey = []byte{prefixMeta, 'r', 'e', 'v'}

	// Iteration bounds.
	logLower = []byte{prefixLog, 0, 0, 0, 0, 0, 0, 0, 0}
	logUpper = []byte{prefixLog + 1}
	curUpper = []byte{prefixCur + 1}
)

const (
	flagCreate = byte(1 << 0)
	flagDelete = byte(1 << 1)
)

// record is the in-memory representation of a pebble log entry.
type record struct {
	key            string
	value          []byte
	createRevision int64
	prevRevision   int64
	lease          int64
	create         bool
	delete         bool
}

// logKey encodes a revision as a pebble log key.
// curKey encodes a key as a pebble 'c' (current) index key.
func curKey(key string) []byte {
	k := make([]byte, 1+len(key))
	k[0] = prefixCur
	copy(k[1:], key)
	return k
}

func curKeyUpper(prefix string) []byte {
	ub := upperBound([]byte(prefix))
	if ub == nil {
		return curUpper
	}
	k := make([]byte, 1+len(ub))
	k[0] = prefixCur
	copy(k[1:], ub)
	return k
}

// encodeCurValue encodes a cur entry as rev(8B BE) + marshalRecord(r).
// The revision is prepended so a single Pebble read returns both the
// mod-revision and the full record without a second lookup.
func encodeCurValue(rev int64, r *record) []byte {
	rec := marshalRecord(r)
	b := make([]byte, 8+len(rec))
	binary.BigEndian.PutUint64(b[:8], uint64(rev))
	copy(b[8:], rec)
	return b
}

// decodeCurValue decodes a cur entry, returning a fully populated KeyValue.
func decodeCurValue(key string, b []byte) (*KeyValue, error) {
	if len(b) < 8 {
		return nil, fmt.Errorf("store: cur value too short (%d bytes)", len(b))
	}
	rev := int64(binary.BigEndian.Uint64(b[:8]))
	r, err := unmarshalRecord(b[8:])
	if err != nil {
		return nil, err
	}
	return &KeyValue{
		Key:            key,
		Value:          r.value,
		Revision:       rev,
		CreateRevision: r.createRevision,
		PrevRevision:   r.prevRevision,
		Lease:          r.lease,
	}, nil
}

// logKey encodes a revision as a pebble log key.
func logKey(rev int64) []byte {
	k := make([]byte, 9)
	k[0] = prefixLog
	binary.BigEndian.PutUint64(k[1:], uint64(rev))
	return k
}

func decodeLogKey(k []byte) int64 {
	return int64(binary.BigEndian.Uint64(k[1:]))
}

func upperBound(prefix []byte) []byte {
	b := make([]byte, len(prefix))
	copy(b, prefix)
	for i := len(b) - 1; i >= 0; i-- {
		b[i]++
		if b[i] != 0 {
			return b[:i+1]
		}
	}
	return nil
}

func encodeRev(rev int64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, uint64(rev))
	return b
}

func decodeRev(b []byte) int64 {
	return int64(binary.BigEndian.Uint64(b))
}

// entryHeaderSize: flags(1) + createRev(8) + prevRev(8) + lease(8) + keyLen(4) = 29
const entryHeaderSize = 29

func marshalRecord(r *record) []byte {
	var flags byte
	if r.create {
		flags |= flagCreate
	}
	if r.delete {
		flags |= flagDelete
	}
	buf := make([]byte, entryHeaderSize+len(r.key)+len(r.value))
	buf[0] = flags
	binary.BigEndian.PutUint64(buf[1:9], uint64(r.createRevision))
	binary.BigEndian.PutUint64(buf[9:17], uint64(r.prevRevision))
	binary.BigEndian.PutUint64(buf[17:25], uint64(r.lease))
	binary.BigEndian.PutUint32(buf[25:29], uint32(len(r.key)))
	copy(buf[29:], r.key)
	copy(buf[29+len(r.key):], r.value)
	return buf
}

func unmarshalRecord(b []byte) (*record, error) {
	if len(b) < entryHeaderSize {
		return nil, fmt.Errorf("store: record too short (%d bytes)", len(b))
	}
	r := &record{}
	flags := b[0]
	r.create = flags&flagCreate != 0
	r.delete = flags&flagDelete != 0
	r.createRevision = int64(binary.BigEndian.Uint64(b[1:9]))
	r.prevRevision = int64(binary.BigEndian.Uint64(b[9:17]))
	r.lease = int64(binary.BigEndian.Uint64(b[17:25]))
	klen := int(binary.BigEndian.Uint32(b[25:29]))
	if len(b) < entryHeaderSize+klen {
		return nil, fmt.Errorf("store: record key truncated")
	}
	r.key = string(b[entryHeaderSize : entryHeaderSize+klen])
	raw := b[entryHeaderSize+klen:]
	if len(raw) > 0 {
		r.value = make([]byte, len(raw))
		copy(r.value, raw)
	}
	return r, nil
}
