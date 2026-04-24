package t4doc

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/t4db/t4/t4doc/t4docpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Server exposes a DB as a gRPC DocumentStore service.
type Server struct {
	t4docpb.UnimplementedDocumentStoreServer
	db        *DB
	authorize Authorizer
}

// Authorizer validates access to a collection.
type Authorizer func(ctx context.Context, collection string, write bool) error

// ServerOption configures a document gRPC server.
type ServerOption func(*Server)

// WithAuthorizer configures collection-level authorization.
func WithAuthorizer(fn Authorizer) ServerOption {
	return func(s *Server) { s.authorize = fn }
}

// NewServer creates a document gRPC server.
func NewServer(db *DB, opts ...ServerOption) *Server {
	s := &Server{db: db}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Register wires the document service onto srv.
func (s *Server) Register(srv *grpc.Server) {
	t4docpb.RegisterDocumentStoreServer(srv, s)
}

func (s *Server) Insert(ctx context.Context, req *t4docpb.WriteDocumentRequest) (*t4docpb.WriteDocumentResponse, error) {
	if err := s.check(ctx, req.Collection, true); err != nil {
		return nil, err
	}
	rev, err := s.db.insert(ctx, req.Collection, req.Id, json.RawMessage(req.Json))
	if err != nil {
		return nil, rpcError(err)
	}
	return &t4docpb.WriteDocumentResponse{Revision: rev}, nil
}

func (s *Server) Put(ctx context.Context, req *t4docpb.WriteDocumentRequest) (*t4docpb.WriteDocumentResponse, error) {
	if err := s.check(ctx, req.Collection, true); err != nil {
		return nil, err
	}
	rev, err := s.db.put(ctx, req.Collection, req.Id, json.RawMessage(req.Json))
	if err != nil {
		return nil, rpcError(err)
	}
	return &t4docpb.WriteDocumentResponse{Revision: rev}, nil
}

func (s *Server) Get(ctx context.Context, req *t4docpb.GetDocumentRequest) (*t4docpb.GetDocumentResponse, error) {
	if err := s.check(ctx, req.Collection, false); err != nil {
		return nil, err
	}
	doc, err := s.db.get(ctx, req.Collection, req.Id)
	if err != nil {
		return nil, rpcError(err)
	}
	return &t4docpb.GetDocumentResponse{Document: storedToProto(req.Collection, doc)}, nil
}

func (s *Server) Delete(ctx context.Context, req *t4docpb.DeleteDocumentRequest) (*t4docpb.WriteDocumentResponse, error) {
	if err := s.check(ctx, req.Collection, true); err != nil {
		return nil, err
	}
	rev, err := s.db.delete(ctx, req.Collection, req.Id)
	if err != nil {
		return nil, rpcError(err)
	}
	return &t4docpb.WriteDocumentResponse{Revision: rev}, nil
}

func (s *Server) Patch(ctx context.Context, req *t4docpb.PatchDocumentRequest) (*t4docpb.WriteDocumentResponse, error) {
	if err := s.check(ctx, req.Collection, true); err != nil {
		return nil, err
	}
	rev, err := s.db.patch(ctx, req.Collection, req.Id, MergePatch(req.MergePatch))
	if err != nil {
		return nil, rpcError(err)
	}
	return &t4docpb.WriteDocumentResponse{Revision: rev}, nil
}

func (s *Server) Find(ctx context.Context, req *t4docpb.FindRequest) (*t4docpb.FindResponse, error) {
	if err := s.check(ctx, req.Collection, false); err != nil {
		return nil, err
	}
	filter, opts, err := requestPlan(req)
	if err != nil {
		return nil, rpcError(err)
	}
	docs, err := s.db.find(ctx, req.Collection, filter, opts)
	if err != nil {
		return nil, rpcError(err)
	}
	resp := &t4docpb.FindResponse{Documents: make([]*t4docpb.Document, len(docs))}
	for i := range docs {
		resp.Documents[i] = storedToProto(req.Collection, &docs[i])
	}
	return resp, nil
}

func (s *Server) FindStream(req *t4docpb.FindRequest, stream t4docpb.DocumentStore_FindStreamServer) error {
	if err := s.check(stream.Context(), req.Collection, false); err != nil {
		return err
	}
	filter, opts, err := requestPlan(req)
	if err != nil {
		return rpcError(err)
	}
	docs, err := s.db.find(stream.Context(), req.Collection, filter, opts)
	if err != nil {
		return rpcError(err)
	}
	for i := range docs {
		if err := stream.Send(storedToProto(req.Collection, &docs[i])); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) Watch(req *t4docpb.WatchRequest, stream t4docpb.DocumentStore_WatchServer) error {
	if err := s.check(stream.Context(), req.Collection, false); err != nil {
		return err
	}
	filter, err := filterFromProto(req.Filter)
	if err != nil {
		return rpcError(err)
	}
	changes, err := s.db.watch(stream.Context(), req.Collection, filter)
	if err != nil {
		return rpcError(err)
	}
	for change := range changes {
		pb := &t4docpb.Change{
			Type:     changeTypeToProto(change.Type),
			Revision: change.Revision,
		}
		if change.Document != nil {
			pb.Document = storedToProto(req.Collection, change.Document)
		}
		if err := stream.Send(pb); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) CreateIndex(ctx context.Context, req *t4docpb.CreateIndexRequest) (*t4docpb.CreateIndexResponse, error) {
	if err := s.check(ctx, req.Collection, true); err != nil {
		return nil, err
	}
	spec, err := indexFromProto(req.Index)
	if err != nil {
		return nil, rpcError(err)
	}
	if err := s.db.createIndex(ctx, req.Collection, spec); err != nil {
		return nil, rpcError(err)
	}
	return &t4docpb.CreateIndexResponse{}, nil
}

func (s *Server) check(ctx context.Context, collection string, write bool) error {
	if s.authorize == nil {
		return nil
	}
	return s.authorize(ctx, collection, write)
}

func requestPlan(req *t4docpb.FindRequest) (Filter, findOptions, error) {
	filter, err := filterFromProto(req.Filter)
	if err != nil {
		return Filter{}, findOptions{}, err
	}
	opts := findOptions{
		limit:     int(req.Limit),
		allowScan: req.AllowScan,
		maxScan:   req.MaxScan,
	}
	if opts.maxScan <= 0 {
		opts.maxScan = defaultMaxScan
	}
	return filter, opts, nil
}

func storedToProto(collection string, doc *storedDocument) *t4docpb.Document {
	return &t4docpb.Document{
		Collection:     collection,
		Id:             doc.ID,
		Json:           doc.Raw,
		Revision:       doc.Revision,
		CreateRevision: doc.CreateRevision,
	}
}

func indexFromProto(pb *t4docpb.IndexSpec) (IndexSpec, error) {
	if pb == nil {
		return IndexSpec{}, ErrInvalidName
	}
	var typ FieldType
	switch pb.Type {
	case t4docpb.IndexSpec_STRING:
		typ = String
	case t4docpb.IndexSpec_INT64:
		typ = Int64
	case t4docpb.IndexSpec_BOOL:
		typ = Bool
	default:
		return IndexSpec{}, status.Error(codes.InvalidArgument, "unsupported index type")
	}
	return IndexSpec{Name: pb.Name, Field: pb.Field, Type: typ}, nil
}

func indexToProto(spec IndexSpec) *t4docpb.IndexSpec {
	pb := &t4docpb.IndexSpec{Name: spec.Name, Field: spec.Field}
	switch spec.Type {
	case String:
		pb.Type = t4docpb.IndexSpec_STRING
	case Int64:
		pb.Type = t4docpb.IndexSpec_INT64
	case Bool:
		pb.Type = t4docpb.IndexSpec_BOOL
	}
	return pb
}

func changeTypeToProto(t ChangeType) t4docpb.Change_Type {
	if t == ChangeDelete {
		return t4docpb.Change_DELETE
	}
	return t4docpb.Change_PUT
}

func rpcError(err error) error {
	switch {
	case errors.Is(err, ErrNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, ErrConflict):
		return status.Error(codes.Aborted, err.Error())
	case errors.Is(err, ErrMissingIndex), errors.Is(err, ErrScanLimitExceeded):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, ErrInvalidName), errors.Is(err, ErrInvalidDocumentID):
		return status.Error(codes.InvalidArgument, err.Error())
	default:
		if _, ok := status.FromError(err); ok {
			return err
		}
		return status.Error(codes.Internal, err.Error())
	}
}
