// Package t4 provides an embeddable, S3-durable key-value store.
package t4

// KeyValue is a versioned key-value pair.
type KeyValue struct {
	Key            string
	Value          []byte
	Revision       int64
	CreateRevision int64
	PrevRevision   int64
	Lease          int64
}

// EventType classifies a watch event.
type EventType int

const (
	EventPut    EventType = iota // create or update
	EventDelete                  // deletion
)

// Event is a single watch notification.
type Event struct {
	Type   EventType
	KV     *KeyValue
	PrevKV *KeyValue // nil for creates
}

// ScanOptions configures a paged scan over live keys.
//
// Prefix scans all live keys under Prefix. Start/End scan the half-open range
// [Start, End). If Prefix is set, Start and End must be empty. After, when
// set, skips all keys up to and including that key and is intended to be the
// cursor returned by a previous Scan call.
type ScanOptions struct {
	Prefix string
	Start  string
	End    string
	After  string
	Limit  int
}
