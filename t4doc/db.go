package t4doc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/t4db/t4"
)

type collectionBackend interface {
	insert(ctx context.Context, collection, id string, raw json.RawMessage) (int64, error)
	put(ctx context.Context, collection, id string, raw json.RawMessage) (int64, error)
	get(ctx context.Context, collection, id string) (*storedDocument, error)
	delete(ctx context.Context, collection, id string) (int64, error)
	patch(ctx context.Context, collection, id string, patch MergePatch) (int64, error)
	find(ctx context.Context, collection string, filter Filter, opts findOptions) ([]storedDocument, error)
	watch(ctx context.Context, collection string, filter Filter) (<-chan storedChange, error)
	createIndex(ctx context.Context, collection string, spec IndexSpec) error
}

// Database is implemented by *DB and *Client. External implementations are not
// part of the V1 API contract.
type Database interface {
	collectionBackend(string) collectionBackend
}

// DB is an embedded document database backed by a local T4 node.
type DB struct {
	node   *t4.Node
	mu     sync.Mutex
	hubs   map[string]*watchHub
	ctx    context.Context
	cancel context.CancelFunc
}

// Open creates an embedded document database over node.
func Open(node *t4.Node) *DB {
	ctx, cancel := context.WithCancel(context.Background())
	return &DB{node: node, hubs: make(map[string]*watchHub), ctx: ctx, cancel: cancel}
}

// Close stops background document watch hubs.
func (db *DB) Close() { db.cancel() }

func (db *DB) collectionBackend(string) collectionBackend { return db }

type storedDocument struct {
	ID             string
	Raw            json.RawMessage
	Revision       int64
	CreateRevision int64
}

type storedChange struct {
	Type     ChangeType
	Document *storedDocument
	Revision int64
}

func (db *DB) loadCatalog(collection string) (catalog, int64, error) {
	kv, err := db.node.Get(catalogKey(collection))
	if err != nil {
		return catalog{}, 0, err
	}
	if kv == nil {
		return emptyCatalog(), 0, nil
	}
	cat, err := decodeCatalog(kv.Value)
	if err != nil {
		return catalog{}, 0, err
	}
	return cat, kv.Revision, nil
}

func (db *DB) get(ctx context.Context, collection, id string) (*storedDocument, error) {
	if err := validateCollectionAndID(collection, id); err != nil {
		return nil, err
	}
	kv, err := db.node.Get(docKey(collection, id))
	if err != nil {
		return nil, err
	}
	if kv == nil {
		return nil, ErrNotFound
	}
	raw, err := decodeEnvelope(kv.Value)
	if err != nil {
		return nil, err
	}
	return &storedDocument{ID: id, Raw: raw, Revision: kv.Revision, CreateRevision: kv.CreateRevision}, nil
}

func validateCollectionAndID(collection, id string) error {
	if err := validateName(collection); err != nil {
		return err
	}
	return validateID(id)
}

func (db *DB) insert(ctx context.Context, collection, id string, raw json.RawMessage) (int64, error) {
	return db.writeDocument(ctx, collection, id, raw, writeInsert)
}

func (db *DB) put(ctx context.Context, collection, id string, raw json.RawMessage) (int64, error) {
	return db.writeDocument(ctx, collection, id, raw, writePut)
}

func (db *DB) patch(ctx context.Context, collection, id string, patch MergePatch) (int64, error) {
	if err := validateCollectionAndID(collection, id); err != nil {
		return 0, err
	}
	for attempt := 0; attempt < 16; attempt++ {
		cur, err := db.get(ctx, collection, id)
		if err != nil {
			return 0, err
		}
		next, err := applyMergePatch(cur.Raw, patch)
		if err != nil {
			return 0, err
		}
		rev, err := db.writeDocumentAt(ctx, collection, id, next, writePatch, cur.Revision)
		if errors.Is(err, ErrConflict) {
			continue
		}
		return rev, err
	}
	return 0, ErrConflict
}

func (db *DB) delete(ctx context.Context, collection, id string) (int64, error) {
	if err := validateCollectionAndID(collection, id); err != nil {
		return 0, err
	}
	for attempt := 0; attempt < 16; attempt++ {
		cat, catRev, err := db.loadCatalog(collection)
		if err != nil {
			return 0, err
		}
		oldKV, err := db.node.Get(docKey(collection, id))
		if err != nil {
			return 0, err
		}
		if oldKV == nil {
			return 0, ErrNotFound
		}
		oldRaw, err := decodeEnvelope(oldKV.Value)
		if err != nil {
			return 0, err
		}
		ops := []t4.TxnOp{{Type: t4.TxnDelete, Key: docKey(collection, id)}}
		for key := range indexKeysForDocument(collection, id, cat.Indexes, oldRaw) {
			ops = append(ops, t4.TxnOp{Type: t4.TxnDelete, Key: key})
		}
		resp, err := db.node.Txn(ctx, t4.TxnRequest{
			Conditions: writeConditions(collection, id, catRev, oldKV.Revision),
			Success:    ops,
		})
		if err != nil {
			return 0, err
		}
		if !resp.Succeeded {
			continue
		}
		return resp.Revision, nil
	}
	return 0, ErrConflict
}

type writeMode uint8

const (
	writeInsert writeMode = iota
	writePut
	writePatch
)

func (db *DB) writeDocument(ctx context.Context, collection, id string, raw json.RawMessage, mode writeMode) (int64, error) {
	return db.writeDocumentAt(ctx, collection, id, raw, mode, 0)
}

func (db *DB) writeDocumentAt(ctx context.Context, collection, id string, raw json.RawMessage, mode writeMode, expectedRev int64) (int64, error) {
	if err := validateCollectionAndID(collection, id); err != nil {
		return 0, err
	}
	if !json.Valid(raw) {
		return 0, fmt.Errorf("t4doc: invalid JSON document")
	}
	for attempt := 0; attempt < 16; attempt++ {
		cat, catRev, err := db.loadCatalog(collection)
		if err != nil {
			return 0, err
		}
		key := docKey(collection, id)
		oldKV, err := db.node.Get(key)
		if err != nil {
			return 0, err
		}
		if mode == writeInsert && oldKV != nil {
			return 0, ErrConflict
		}
		if mode == writePatch && oldKV == nil {
			return 0, ErrNotFound
		}
		var oldRaw json.RawMessage
		var oldRev int64
		if oldKV != nil {
			oldRev = oldKV.Revision
			if expectedRev > 0 && oldRev != expectedRev {
				return 0, ErrConflict
			}
			oldRaw, err = decodeEnvelope(oldKV.Value)
			if err != nil {
				return 0, err
			}
		}
		env, err := envelopeFromRaw(raw)
		if err != nil {
			return 0, err
		}
		ops := []t4.TxnOp{{Type: t4.TxnPut, Key: key, Value: env}}
		oldIndexes := indexKeysForDocument(collection, id, cat.Indexes, oldRaw)
		newIndexes := indexKeysForDocument(collection, id, cat.Indexes, raw)
		for k := range oldIndexes {
			if _, keep := newIndexes[k]; !keep {
				ops = append(ops, t4.TxnOp{Type: t4.TxnDelete, Key: k})
			}
		}
		for k := range newIndexes {
			if _, exists := oldIndexes[k]; !exists {
				ops = append(ops, t4.TxnOp{Type: t4.TxnPut, Key: k, Value: []byte("1")})
			}
		}
		resp, err := db.node.Txn(ctx, t4.TxnRequest{
			Conditions: writeConditions(collection, id, catRev, oldRev),
			Success:    ops,
		})
		if err != nil {
			return 0, err
		}
		if !resp.Succeeded {
			continue
		}
		return resp.Revision, nil
	}
	return 0, ErrConflict
}

func writeConditions(collection, id string, catalogRev, docRev int64) []t4.TxnCondition {
	conds := []t4.TxnCondition{revisionCondition(catalogKey(collection), catalogRev)}
	return append(conds, revisionCondition(docKey(collection, id), docRev))
}

func revisionCondition(key string, rev int64) t4.TxnCondition {
	if rev == 0 {
		return t4.TxnCondition{Key: key, Target: t4.TxnCondVersion, Result: t4.TxnCondEqual, Version: 0}
	}
	return t4.TxnCondition{Key: key, Target: t4.TxnCondMod, Result: t4.TxnCondEqual, ModRevision: rev}
}

func indexKeysForDocument(collection, id string, indexes []IndexSpec, raw json.RawMessage) map[string]struct{} {
	keys := make(map[string]struct{})
	if len(raw) == 0 {
		return keys
	}
	for _, idx := range indexes {
		if idx.State != IndexBuilding && idx.State != IndexReady {
			continue
		}
		value, ok := extractJSONField(raw, idx.Field)
		if !ok {
			continue
		}
		encoded, ok := encodeIndexValue(idx, value)
		if !ok {
			continue
		}
		keys[indexKey(collection, idx.Name, encoded, id)] = struct{}{}
	}
	return keys
}
