package t4doc

import (
	"context"
	"encoding/json"
)

// Collection is a typed document collection.
type Collection[T any] struct {
	backend collectionBackend
	name    string
}

// NewCollection returns a typed collection handle.
func NewCollection[T any](db Database, name string) *Collection[T] {
	return &Collection[T]{backend: db.collectionBackend(name), name: name}
}

// Insert creates a document and fails if the ID already exists.
func (c *Collection[T]) Insert(ctx context.Context, id string, doc T) (int64, error) {
	_, raw, err := marshalEnvelope(doc)
	if err != nil {
		return 0, err
	}
	return c.backend.insert(ctx, c.name, id, raw)
}

// Put creates or replaces a document.
func (c *Collection[T]) Put(ctx context.Context, id string, doc T) (int64, error) {
	_, raw, err := marshalEnvelope(doc)
	if err != nil {
		return 0, err
	}
	return c.backend.put(ctx, c.name, id, raw)
}

// FindByID returns a document by ID.
func (c *Collection[T]) FindByID(ctx context.Context, id string) (*Document[T], error) {
	stored, err := c.backend.get(ctx, c.name, id)
	if err != nil {
		return nil, err
	}
	return typedDocument[T](stored)
}

// FindOne returns the first matching document.
func (c *Collection[T]) FindOne(ctx context.Context, filter Filter, opts ...FindOption) (*Document[T], error) {
	opts = append(opts, Limit(1))
	docs, err := c.Find(ctx, filter, opts...)
	if err != nil {
		return nil, err
	}
	if len(docs) == 0 {
		return nil, ErrNotFound
	}
	return &docs[0], nil
}

// Find returns matching documents.
func (c *Collection[T]) Find(ctx context.Context, filter Filter, opts ...FindOption) ([]Document[T], error) {
	stored, err := c.backend.find(ctx, c.name, filter, collectFindOptions(opts))
	if err != nil {
		return nil, err
	}
	out := make([]Document[T], 0, len(stored))
	for i := range stored {
		doc, err := typedDocument[T](&stored[i])
		if err != nil {
			return nil, err
		}
		out = append(out, *doc)
	}
	return out, nil
}

// Patch applies a JSON merge patch to a document.
func (c *Collection[T]) Patch(ctx context.Context, id string, patch MergePatch) (int64, error) {
	return c.backend.patch(ctx, c.name, id, patch)
}

// Delete removes a document.
func (c *Collection[T]) Delete(ctx context.Context, id string) (int64, error) {
	return c.backend.delete(ctx, c.name, id)
}

// CreateIndex creates and synchronously backfills an equality index.
func (c *Collection[T]) CreateIndex(ctx context.Context, spec IndexSpec) error {
	return c.backend.createIndex(ctx, c.name, spec)
}

// Watch returns changes matching filter from now.
func (c *Collection[T]) Watch(ctx context.Context, filter Filter) (<-chan Change[T], error) {
	rawCh, err := c.backend.watch(ctx, c.name, filter)
	if err != nil {
		return nil, err
	}
	out := make(chan Change[T], 64)
	go func() {
		defer close(out)
		for ch := range rawCh {
			var doc *Document[T]
			if ch.Document != nil {
				typed, err := typedDocument[T](ch.Document)
				if err != nil {
					continue
				}
				doc = typed
			}
			select {
			case out <- Change[T]{Type: ch.Type, Document: doc, Revision: ch.Revision}:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

func typedDocument[T any](stored *storedDocument) (*Document[T], error) {
	var value T
	if err := json.Unmarshal(stored.Raw, &value); err != nil {
		return nil, err
	}
	return &Document[T]{
		ID:             stored.ID,
		Value:          value,
		Revision:       stored.Revision,
		CreateRevision: stored.CreateRevision,
	}, nil
}
