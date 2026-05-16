package etcd

import (
	"context"
	"reflect"
	"testing"
	"time"

	"go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/api/v3/mvccpb"

	"github.com/t4db/t4"
)

// mkEvent returns an mvccpb.Event whose Kv.Value pads the event to roughly
// approxBytes total size as estimated by estimateEventSize.
func mkEvent(rev int64, approxBytes int) *mvccpb.Event {
	padding := approxBytes - 64 // back out the overhead estimateEventSize adds
	if padding < 0 {
		padding = 0
	}
	return &mvccpb.Event{
		Type: mvccpb.PUT,
		Kv: &mvccpb.KeyValue{
			Key:         []byte("/k"),
			Value:       make([]byte, padding),
			ModRevision: rev,
		},
	}
}

func TestSplitEventsBySizeSingleChunkUnderBudget(t *testing.T) {
	events := []*mvccpb.Event{mkEvent(1, 100), mkEvent(2, 100), mkEvent(3, 100)}
	chunks := splitEventsBySize(events, 1<<20)
	if len(chunks) != 1 {
		t.Fatalf("got %d chunks, want 1", len(chunks))
	}
	if !reflect.DeepEqual(chunks[0], events) {
		t.Fatalf("chunk 0 != original input")
	}
}

func TestSplitEventsBySizeMultipleChunks(t *testing.T) {
	// Each event ~256 bytes; budget 600 → ~2 events per chunk → 5 chunks for 10 events.
	const eventSize = 256
	const budget = 600
	events := make([]*mvccpb.Event, 10)
	for i := range events {
		events[i] = mkEvent(int64(i+1), eventSize)
	}
	chunks := splitEventsBySize(events, budget)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}
	// Reassembling chunks reproduces the original order and count.
	reassembled := make([]*mvccpb.Event, 0, len(events))
	for _, c := range chunks {
		reassembled = append(reassembled, c...)
	}
	if !reflect.DeepEqual(reassembled, events) {
		t.Fatalf("reassembled events differ from input")
	}
	// Every chunk except possibly the last must fit the budget. (The last
	// chunk may contain a single oversize event by design.)
	for i, c := range chunks {
		size := estimateEventsSize(c)
		if i < len(chunks)-1 && size > budget {
			t.Errorf("chunk %d size %d > budget %d", i, size, budget)
		}
	}
}

func TestSplitEventsBySizeOversizeSingleEvent(t *testing.T) {
	// An event larger than the budget must still be emitted as a single
	// chunk on its own — we cannot split inside one event.
	big := mkEvent(1, 4096)
	small := mkEvent(2, 100)
	chunks := splitEventsBySize([]*mvccpb.Event{big, small}, 1024)
	if len(chunks) != 2 {
		t.Fatalf("got %d chunks, want 2 (big alone, then small)", len(chunks))
	}
	if len(chunks[0]) != 1 || chunks[0][0] != big {
		t.Errorf("chunk 0 should contain the oversize event alone: %+v", chunks[0])
	}
	if len(chunks[1]) != 1 || chunks[1][0] != small {
		t.Errorf("chunk 1 should contain the trailing small event: %+v", chunks[1])
	}
}

// TestSendEventsFragmentFlag drives sendEvents directly to verify the
// Fragment=true/false pattern across a multi-fragment send. Goes through a
// real Server (so headerAt works) but uses a private sendCh so we can see
// every WatchResponse the server tries to push.
func TestSendEventsFragmentFlag(t *testing.T) {
	node, err := t4.Open(t4.Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("t4.Open: %v", err)
	}
	t.Cleanup(func() { _ = node.Close() })
	srv := New(node, nil, nil)

	const eventSize = 256
	events := make([]*mvccpb.Event, 8)
	for i := range events {
		events[i] = mkEvent(int64(i+1), eventSize)
	}
	// Shrink the fragment budget for the duration of this test so a tiny
	// payload exercises the multi-fragment branch.
	prev := watchFragmentBytes
	watchFragmentBytes = 600
	t.Cleanup(func() { watchFragmentBytes = prev })

	chunks := splitEventsBySize(events, watchFragmentBytes)
	if len(chunks) < 2 {
		t.Fatalf("test setup: expected >1 chunks for budget=%d, got %d", watchFragmentBytes, len(chunks))
	}

	sendCh := make(chan *etcdserverpb.WatchResponse, 16)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if !srv.sendEvents(ctx, sendCh, 99, events, 8, true) {
		t.Fatalf("sendEvents returned false unexpectedly")
	}
	close(sendCh)

	var received []*etcdserverpb.WatchResponse
	for r := range sendCh {
		received = append(received, r)
	}
	if len(received) != len(chunks) {
		t.Fatalf("frames: got %d, want %d", len(received), len(chunks))
	}
	for i, r := range received {
		wantFragment := i < len(received)-1
		if r.Fragment != wantFragment {
			t.Errorf("frame %d Fragment: got %v, want %v", i, r.Fragment, wantFragment)
		}
		if r.WatchId != 99 {
			t.Errorf("frame %d WatchId: got %d, want 99", i, r.WatchId)
		}
		if r.Header == nil {
			t.Errorf("frame %d has nil Header", i)
		}
	}
	// Reassembled event order matches input.
	var got []*mvccpb.Event
	for _, r := range received {
		got = append(got, r.Events...)
	}
	if !reflect.DeepEqual(got, events) {
		t.Fatalf("reassembled events differ from input (got %d, want %d)", len(got), len(events))
	}
}

func TestEstimateEventSizeScalesWithPayload(t *testing.T) {
	small := estimateEventSize(mkEvent(1, 64))
	large := estimateEventSize(mkEvent(1, 64+1024))
	if large <= small {
		t.Fatalf("size estimate should grow with payload: small=%d large=%d", small, large)
	}
	if large-small < 1024 {
		t.Fatalf("size estimate must account for payload bytes: delta=%d (want >= 1024)", large-small)
	}
}
