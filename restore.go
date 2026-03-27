package strata

import "github.com/makhov/strata/pkg/object"

// PinnedObject identifies a specific version of an object in object storage.
type PinnedObject struct {
	Key       string
	VersionID string
}

// RestorePoint describes a precise point in time from which a node should
// bootstrap. When set in Config, the node restores the checkpoint and replays
// the listed WAL segments using their pinned S3 version IDs, rather than
// reading the latest objects from its own prefix.
//
// This enables point-in-time restore, blue/green deployments, and copy-free
// forking: the source data is read directly from S3 by version ID — no objects
// are copied to the new prefix.
//
// The node's own ObjectStore prefix is used for all subsequent writes after
// startup. RestorePoint is only applied on first boot (when the local data
// directory does not yet exist); it is ignored on subsequent restarts.
//
// S3 versioning must be enabled on the source bucket.
type RestorePoint struct {
	// Store is the versioned object store to read pinned objects from.
	// It may use a different prefix than Config.ObjectStore (e.g. to read
	// from the source branch while writing to a new branch prefix).
	Store object.VersionedStore

	// CheckpointArchive is the pinned checkpoint archive object.
	CheckpointArchive PinnedObject

	// WALSegments are the WAL segments to replay after the checkpoint,
	// in ascending sequence order.
	WALSegments []PinnedObject
}
