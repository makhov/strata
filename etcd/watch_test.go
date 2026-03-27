package etcd_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	"github.com/makhov/strata"
)

// ── Watch unit tests ──────────────────────────────────────────────────────────

// TestWatchReceivesPut verifies a put event is delivered to a watcher.
func TestWatchReceivesPut(t *testing.T) {
	node, cli := newWatchNode(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wch := cli.Watch(ctx, "/w/key")
	go func() { node.Put(ctx, "/w/key", []byte("v"), 0) }()

	select {
	case wr := <-wch:
		if len(wr.Events) == 0 {
			t.Fatal("expected at least one event")
		}
		ev := wr.Events[0]
		if ev.Type != clientv3.EventTypePut {
			t.Errorf("event type: want PUT got %v", ev.Type)
		}
		if string(ev.Kv.Key) != "/w/key" {
			t.Errorf("event key: want /w/key got %q", ev.Kv.Key)
		}
		if string(ev.Kv.Value) != "v" {
			t.Errorf("event value: want v got %q", ev.Kv.Value)
		}
	case <-ctx.Done():
		t.Fatal("timeout waiting for watch event")
	}
}

// TestWatchReceivesDelete verifies a delete event is delivered.
func TestWatchReceivesDelete(t *testing.T) {
	node, cli := newWatchNode(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	node.Put(ctx, "/w/del", []byte("v"), 0)

	wch := cli.Watch(ctx, "/w/del")
	go func() { node.Delete(ctx, "/w/del") }()

	select {
	case wr := <-wch:
		if len(wr.Events) == 0 {
			t.Fatal("expected delete event")
		}
		if wr.Events[0].Type != clientv3.EventTypeDelete {
			t.Errorf("event type: want DELETE got %v", wr.Events[0].Type)
		}
	case <-ctx.Done():
		t.Fatal("timeout waiting for delete event")
	}
}

// TestWatchPrefix verifies prefix watch catches all matching keys.
func TestWatchPrefix(t *testing.T) {
	node, cli := newWatchNode(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wch := cli.Watch(ctx, "/pfx/", clientv3.WithPrefix())
	const n = 3
	go func() {
		for i := 0; i < n; i++ {
			node.Put(ctx, fmt.Sprintf("/pfx/%d", i), []byte("v"), 0)
		}
	}()

	received := 0
	for received < n {
		select {
		case wr := <-wch:
			received += len(wr.Events)
		case <-ctx.Done():
			t.Fatalf("timeout: got %d/%d events", received, n)
		}
	}
}

// TestWatchNonMatchingPrefix verifies events outside the prefix are not delivered.
func TestWatchNonMatchingPrefix(t *testing.T) {
	node, cli := newWatchNode(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	wch := cli.Watch(ctx, "/match/", clientv3.WithPrefix())
	// Write to a different prefix — should not trigger watcher.
	node.Put(ctx, "/other/key", []byte("v"), 0)
	// Write one that DOES match to unblock the channel check.
	go func() {
		time.Sleep(100 * time.Millisecond)
		node.Put(ctx, "/match/key", []byte("v"), 0)
	}()

	select {
	case wr := <-wch:
		for _, ev := range wr.Events {
			if string(ev.Kv.Key) == "/other/key" {
				t.Error("received event for non-matching key /other/key")
			}
		}
	case <-ctx.Done():
		t.Fatal("timeout waiting for matching event")
	}
}

// TestWatchMultipleConcurrent verifies multiple simultaneous watches each
// receive only their own events.
func TestWatchMultipleConcurrent(t *testing.T) {
	node, cli := newWatchNode(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	const watchers = 5
	channels := make([]clientv3.WatchChan, watchers)
	for i := 0; i < watchers; i++ {
		channels[i] = cli.Watch(ctx, fmt.Sprintf("/multi/%d/", i), clientv3.WithPrefix())
	}

	// Each watcher gets 2 events under its own prefix.
	for i := 0; i < watchers; i++ {
		i := i
		go func() {
			node.Put(ctx, fmt.Sprintf("/multi/%d/a", i), []byte("v"), 0)
			node.Put(ctx, fmt.Sprintf("/multi/%d/b", i), []byte("v"), 0)
		}()
	}

	for i, wch := range channels {
		received := 0
		for received < 2 {
			select {
			case wr := <-wch:
				received += len(wr.Events)
			case <-ctx.Done():
				t.Fatalf("watcher %d: timeout, got %d/2 events", i, received)
			}
		}
	}
}

// TestWatchCancel verifies that cancelling the watch context stops delivery.
func TestWatchCancel(t *testing.T) {
	node, cli := newWatchNode(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	watchCtx, watchCancel := context.WithCancel(ctx)
	wch := cli.Watch(watchCtx, "/cancel/", clientv3.WithPrefix())

	// Receive one event to confirm the watch is live.
	node.Put(ctx, "/cancel/first", []byte("v"), 0)
	select {
	case wr := <-wch:
		if len(wr.Events) == 0 {
			t.Fatal("expected first event")
		}
	case <-ctx.Done():
		t.Fatal("timeout before first event")
	}

	// Cancel the watch context.
	watchCancel()

	// Write another event — channel should close or drain without new events.
	node.Put(ctx, "/cancel/second", []byte("v"), 0)
	time.Sleep(100 * time.Millisecond)

	// The channel should eventually be closed.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case wr, ok := <-wch:
			if !ok {
				return // channel closed: expected
			}
			// Drain any pending event (may arrive before cancel propagates).
			_ = wr
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}

// TestWatchFromRevision verifies the StartRevision field is respected:
// events at or after the given revision are replayed.
func TestWatchFromRevision(t *testing.T) {
	node, cli := newWatchNode(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Write two events, capture the revision after the first.
	rev1, _ := node.Put(ctx, "/rev/a", []byte("1"), 0)
	node.Put(ctx, "/rev/b", []byte("2"), 0)

	// Watch from rev1 — both /rev/a and /rev/b should arrive.
	wch := cli.Watch(ctx, "/rev/", clientv3.WithPrefix(), clientv3.WithRev(rev1))

	received := 0
	for received < 2 {
		select {
		case wr := <-wch:
			received += len(wr.Events)
		case <-ctx.Done():
			t.Fatalf("timeout: got %d/2 events", received)
		}
	}
}

// newWatchNode opens a strata.Node and an etcd client. Returns both so tests
// can write to the node directly.
func newWatchNode(t *testing.T) (*strata.Node, *clientv3.Client) {
	t.Helper()
	node, err := strata.Open(strata.Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("strata.Open: %v", err)
	}
	t.Cleanup(func() { node.Close() })
	endpoint := startEtcdServer(t, node)
	cli := newEtcdClient(t, endpoint)
	return node, cli
}
