package t4doc

import (
	"context"
	"fmt"

	"github.com/t4db/t4"
)

func (db *DB) createIndex(ctx context.Context, collection string, spec IndexSpec) error {
	if err := validateName(collection); err != nil {
		return err
	}
	spec, err := normalizeIndexSpec(spec, IndexBuilding)
	if err != nil {
		return err
	}

	for attempt := 0; attempt < 16; attempt++ {
		cat, catRev, err := db.loadCatalog(collection)
		if err != nil {
			return err
		}
		if existing, _, ok := cat.indexByName(spec.Name); ok {
			if existing.Field == spec.Field && existing.Type == spec.Type {
				if existing.State == IndexReady {
					return nil
				}
				if err := db.backfillIndex(ctx, collection, existing); err != nil {
					return err
				}
				return db.markIndexReady(ctx, collection, existing.Name)
			}
			return fmt.Errorf("t4doc: index %q already exists with different spec", spec.Name)
		}
		cat.Indexes = append(cat.Indexes, spec)
		data, err := cat.encode()
		if err != nil {
			return err
		}
		resp, err := db.node.Txn(ctx, t4.TxnRequest{
			Conditions: []t4.TxnCondition{revisionCondition(catalogKey(collection), catRev)},
			Success:    []t4.TxnOp{{Type: t4.TxnPut, Key: catalogKey(collection), Value: data}},
		})
		if err != nil {
			return err
		}
		if !resp.Succeeded {
			continue
		}
		if err := db.backfillIndex(ctx, collection, spec); err != nil {
			return err
		}
		return db.markIndexReady(ctx, collection, spec.Name)
	}
	return ErrConflict
}

func (db *DB) backfillIndex(ctx context.Context, collection string, spec IndexSpec) error {
	var after string
	for {
		kvs, cursor, err := db.node.Scan(ctx, t4.ScanOptions{Prefix: docPrefix(collection), After: after, Limit: scanPageSize})
		if err != nil {
			return err
		}
		if len(kvs) == 0 {
			return nil
		}
		ops := make([]t4.TxnOp, 0, len(kvs))
		for _, kv := range kvs {
			id, err := idFromDocKey(collection, kv.Key)
			if err != nil {
				return err
			}
			raw, err := decodeEnvelope(kv.Value)
			if err != nil {
				return err
			}
			keys := indexKeysForDocument(collection, id, []IndexSpec{spec}, raw)
			for key := range keys {
				ops = append(ops, t4.TxnOp{Type: t4.TxnPut, Key: key, Value: []byte("1")})
			}
		}
		if len(ops) > 0 {
			if _, err := db.node.Txn(ctx, t4.TxnRequest{Success: ops}); err != nil {
				return err
			}
		}
		after = cursor
	}
}

func (db *DB) markIndexReady(ctx context.Context, collection, indexName string) error {
	for attempt := 0; attempt < 16; attempt++ {
		cat, catRev, err := db.loadCatalog(collection)
		if err != nil {
			return err
		}
		idx, pos, ok := cat.indexByName(indexName)
		if !ok {
			return ErrNotFound
		}
		if idx.State == IndexReady {
			return nil
		}
		cat.Indexes[pos].State = IndexReady
		data, err := cat.encode()
		if err != nil {
			return err
		}
		resp, err := db.node.Txn(ctx, t4.TxnRequest{
			Conditions: []t4.TxnCondition{revisionCondition(catalogKey(collection), catRev)},
			Success:    []t4.TxnOp{{Type: t4.TxnPut, Key: catalogKey(collection), Value: data}},
		})
		if err != nil {
			return err
		}
		if resp.Succeeded {
			return nil
		}
	}
	return ErrConflict
}
