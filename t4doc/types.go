package t4doc

import (
	"errors"
	"regexp"
	"unicode/utf8"
)

var (
	ErrNotFound          = errors.New("t4doc: document not found")
	ErrConflict          = errors.New("t4doc: document conflict")
	ErrMissingIndex      = errors.New("t4doc: missing index")
	ErrScanLimitExceeded = errors.New("t4doc: scan limit exceeded")
	ErrInvalidName       = errors.New("t4doc: invalid name")
	ErrInvalidDocumentID = errors.New("t4doc: invalid document id")
)

var nameRE = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)

// FieldType is the type of an indexed JSON field.
type FieldType string

const (
	String FieldType = "string"
	Int64  FieldType = "int64"
	Bool   FieldType = "bool"
)

// IndexState is the lifecycle state of an index.
type IndexState string

const (
	IndexBuilding IndexState = "BUILDING"
	IndexReady    IndexState = "READY"
)

// IndexSpec describes a single-field equality index.
type IndexSpec struct {
	Name  string     `json:"name"`
	Field string     `json:"field"`
	Type  FieldType  `json:"type"`
	State IndexState `json:"state,omitempty"`
}

// Document is a typed JSON document with T4 revision metadata.
type Document[T any] struct {
	ID             string
	Value          T
	Revision       int64
	CreateRevision int64
}

// Change is a document watch notification.
type Change[T any] struct {
	Type     ChangeType
	Document *Document[T]
	Revision int64
}

// ChangeType classifies document watch notifications.
type ChangeType int

const (
	ChangePut ChangeType = iota + 1
	ChangeDelete
)

// MergePatch is an RFC 7396-style JSON merge patch.
type MergePatch []byte

func validateName(name string) error {
	if !nameRE.MatchString(name) {
		return ErrInvalidName
	}
	return nil
}

func validateID(id string) error {
	if id == "" || !utf8.ValidString(id) {
		return ErrInvalidDocumentID
	}
	return nil
}
