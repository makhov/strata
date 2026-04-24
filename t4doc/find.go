package t4doc

import (
	"context"
	"errors"

	"github.com/t4db/t4"
)

const scanPageSize = 512

func (db *DB) find(ctx context.Context, collection string, filter Filter, opts findOptions) ([]storedDocument, error) {
	if err := validateName(collection); err != nil {
		return nil, err
	}
	cat, _, err := db.loadCatalog(collection)
	if err != nil {
		return nil, err
	}
	idx, encoded, ok := cat.findReadyIndexForFilter(filter)
	if ok {
		return db.findByIndex(ctx, collection, idx, encoded, filter, opts)
	}
	if !opts.allowScan {
		return nil, ErrMissingIndex
	}
	return db.findByCollectionScan(ctx, collection, filter, opts)
}

func (db *DB) findByIndex(ctx context.Context, collection string, idx IndexSpec, encoded string, filter Filter, opts findOptions) ([]storedDocument, error) {
	var out []storedDocument
	var after string
	prefix := indexPrefix(collection, idx.Name, encoded)
	for {
		limit := scanPageSize
		if opts.limit > 0 && len(out) < opts.limit && opts.limit-len(out) < limit {
			limit = opts.limit - len(out)
		}
		kvs, cursor, err := db.node.Scan(ctx, t4.ScanOptions{Prefix: prefix, After: after, Limit: limit})
		if err != nil {
			return nil, err
		}
		if len(kvs) == 0 {
			return out, nil
		}
		for _, kv := range kvs {
			id, err := idFromIndexKey(kv.Key)
			if err != nil {
				return nil, err
			}
			doc, err := db.get(ctx, collection, id)
			if errors.Is(err, ErrNotFound) {
				continue
			}
			if err != nil {
				return nil, err
			}
			if filter.match(doc.Raw) {
				out = append(out, *doc)
				if opts.limit > 0 && len(out) >= opts.limit {
					return out, nil
				}
			}
		}
		after = cursor
	}
}

func (db *DB) findByCollectionScan(ctx context.Context, collection string, filter Filter, opts findOptions) ([]storedDocument, error) {
	var out []storedDocument
	var after string
	var scanned int64
	for {
		remaining := opts.maxScan - scanned
		if remaining <= 0 {
			kvs, _, err := db.node.Scan(ctx, t4.ScanOptions{Prefix: docPrefix(collection), After: after, Limit: 1})
			if err != nil {
				return nil, err
			}
			if len(kvs) == 0 {
				return out, nil
			}
			return nil, ErrScanLimitExceeded
		}
		limit := scanPageSize
		if remaining < int64(limit) {
			limit = int(remaining)
		}
		kvs, cursor, err := db.node.Scan(ctx, t4.ScanOptions{Prefix: docPrefix(collection), After: after, Limit: limit})
		if err != nil {
			return nil, err
		}
		if len(kvs) == 0 {
			return out, nil
		}
		for _, kv := range kvs {
			scanned++
			id, err := idFromDocKey(collection, kv.Key)
			if err != nil {
				return nil, err
			}
			raw, err := decodeEnvelope(kv.Value)
			if err != nil {
				return nil, err
			}
			if filter.match(raw) {
				out = append(out, storedDocument{
					ID:             id,
					Raw:            raw,
					Revision:       kv.Revision,
					CreateRevision: kv.CreateRevision,
				})
				if opts.limit > 0 && len(out) >= opts.limit {
					return out, nil
				}
			}
		}
		after = cursor
	}
}
