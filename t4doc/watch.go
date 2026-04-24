package t4doc

import (
	"context"
	"sync"

	"github.com/t4db/t4"
)

type watchHub struct {
	db         *DB
	collection string
	prefix     string

	mu      sync.Mutex
	started bool
	nextID  int64
	subs    map[int64]*watchSub
}

type watchSub struct {
	ctx    context.Context
	filter Filter
	ch     chan storedChange
}

func (db *DB) watch(ctx context.Context, collection string, filter Filter) (<-chan storedChange, error) {
	if err := validateName(collection); err != nil {
		return nil, err
	}
	h := db.hub(collection)
	return h.subscribe(ctx, filter)
}

func (db *DB) hub(collection string) *watchHub {
	db.mu.Lock()
	defer db.mu.Unlock()
	if h := db.hubs[collection]; h != nil {
		return h
	}
	h := &watchHub{
		db:         db,
		collection: collection,
		prefix:     docPrefix(collection),
		subs:       make(map[int64]*watchSub),
	}
	db.hubs[collection] = h
	return h
}

func (h *watchHub) subscribe(ctx context.Context, filter Filter) (<-chan storedChange, error) {
	h.mu.Lock()
	h.nextID++
	id := h.nextID
	sub := &watchSub{ctx: ctx, filter: filter, ch: make(chan storedChange, 64)}
	h.subs[id] = sub
	if !h.started {
		h.started = true
		go h.run()
	}
	h.mu.Unlock()

	go func() {
		<-ctx.Done()
		h.mu.Lock()
		if cur := h.subs[id]; cur != nil {
			delete(h.subs, id)
			close(cur.ch)
		}
		h.mu.Unlock()
	}()
	return sub.ch, nil
}

func (h *watchHub) run() {
	events, err := h.db.node.Watch(h.db.ctx, h.prefix, 0)
	if err != nil {
		h.closeAll()
		return
	}
	for ev := range events {
		change, ok := h.changeFromEvent(ev)
		if !ok {
			continue
		}
		h.broadcast(change)
	}
	h.closeAll()
}

func (h *watchHub) changeFromEvent(ev t4.Event) (storedChange, bool) {
	if ev.KV == nil {
		return storedChange{}, false
	}
	id, err := idFromDocKey(h.collection, ev.KV.Key)
	if err != nil {
		return storedChange{}, false
	}
	var kv *t4.KeyValue
	typ := ChangePut
	if ev.Type == t4.EventDelete {
		typ = ChangeDelete
		kv = ev.PrevKV
	} else {
		kv = ev.KV
	}
	if kv == nil {
		return storedChange{Type: typ, Revision: ev.KV.Revision}, true
	}
	raw, err := decodeEnvelope(kv.Value)
	if err != nil {
		return storedChange{}, false
	}
	return storedChange{
		Type: typ,
		Document: &storedDocument{
			ID:             id,
			Raw:            raw,
			Revision:       kv.Revision,
			CreateRevision: kv.CreateRevision,
		},
		Revision: ev.KV.Revision,
	}, true
}

func (h *watchHub) broadcast(change storedChange) {
	h.mu.Lock()
	subs := make([]*watchSub, 0, len(h.subs))
	for _, sub := range h.subs {
		subs = append(subs, sub)
	}
	h.mu.Unlock()

	for _, sub := range subs {
		if change.Document != nil && !sub.filter.match(change.Document.Raw) {
			continue
		}
		select {
		case sub.ch <- change:
		case <-sub.ctx.Done():
		case <-h.db.ctx.Done():
			return
		}
	}
}

func (h *watchHub) closeAll() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, sub := range h.subs {
		delete(h.subs, id)
		close(sub.ch)
	}
}
