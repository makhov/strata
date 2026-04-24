package t4doc

import (
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/t4db/t4/t4doc/t4docpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// Client is a remote document database client.
type Client struct {
	conn  *grpc.ClientConn
	stub  t4docpb.DocumentStoreClient
	token string
}

// Dial connects to a standalone t4 server exposing the document service.
func Dial(ctx context.Context, target string, opts ...grpc.DialOption) (*Client, error) {
	conn, err := grpc.DialContext(ctx, target, opts...)
	if err != nil {
		return nil, err
	}
	c := &Client{conn: conn, stub: t4docpb.NewDocumentStoreClient(conn)}
	return c, nil
}

// Close closes the underlying gRPC connection.
func (c *Client) Close() error { return c.conn.Close() }

// SetToken configures the bearer token sent to servers with auth enabled.
func (c *Client) SetToken(token string) { c.token = token }

func (c *Client) collectionBackend(string) collectionBackend { return c }

func (c *Client) rpcCtx(ctx context.Context) context.Context {
	if c.token == "" {
		return ctx
	}
	return metadata.AppendToOutgoingContext(ctx, "token", c.token)
}

func (c *Client) insert(ctx context.Context, collection, id string, raw json.RawMessage) (int64, error) {
	resp, err := c.stub.Insert(c.rpcCtx(ctx), &t4docpb.WriteDocumentRequest{Collection: collection, Id: id, Json: raw})
	if err != nil {
		return 0, clientError(err)
	}
	return resp.Revision, nil
}

func (c *Client) put(ctx context.Context, collection, id string, raw json.RawMessage) (int64, error) {
	resp, err := c.stub.Put(c.rpcCtx(ctx), &t4docpb.WriteDocumentRequest{Collection: collection, Id: id, Json: raw})
	if err != nil {
		return 0, clientError(err)
	}
	return resp.Revision, nil
}

func (c *Client) get(ctx context.Context, collection, id string) (*storedDocument, error) {
	resp, err := c.stub.Get(c.rpcCtx(ctx), &t4docpb.GetDocumentRequest{Collection: collection, Id: id})
	if err != nil {
		return nil, clientError(err)
	}
	return protoToStored(resp.Document), nil
}

func (c *Client) delete(ctx context.Context, collection, id string) (int64, error) {
	resp, err := c.stub.Delete(c.rpcCtx(ctx), &t4docpb.DeleteDocumentRequest{Collection: collection, Id: id})
	if err != nil {
		return 0, clientError(err)
	}
	return resp.Revision, nil
}

func (c *Client) patch(ctx context.Context, collection, id string, patch MergePatch) (int64, error) {
	resp, err := c.stub.Patch(c.rpcCtx(ctx), &t4docpb.PatchDocumentRequest{Collection: collection, Id: id, MergePatch: patch})
	if err != nil {
		return 0, clientError(err)
	}
	return resp.Revision, nil
}

func (c *Client) find(ctx context.Context, collection string, filter Filter, opts findOptions) ([]storedDocument, error) {
	req, err := findRequest(collection, filter, opts)
	if err != nil {
		return nil, err
	}
	resp, err := c.stub.Find(c.rpcCtx(ctx), req)
	if err != nil {
		return nil, clientError(err)
	}
	out := make([]storedDocument, 0, len(resp.Documents))
	for _, doc := range resp.Documents {
		out = append(out, *protoToStored(doc))
	}
	return out, nil
}

func (c *Client) watch(ctx context.Context, collection string, filter Filter) (<-chan storedChange, error) {
	pbFilter, err := filterToProto(filter)
	if err != nil {
		return nil, err
	}
	stream, err := c.stub.Watch(c.rpcCtx(ctx), &t4docpb.WatchRequest{Collection: collection, Filter: pbFilter})
	if err != nil {
		return nil, clientError(err)
	}
	out := make(chan storedChange, 64)
	go func() {
		defer close(out)
		for {
			pb, err := stream.Recv()
			if errors.Is(err, io.EOF) {
				return
			}
			if err != nil {
				return
			}
			ch := storedChange{Type: ChangePut, Revision: pb.Revision}
			if pb.Type == t4docpb.Change_DELETE {
				ch.Type = ChangeDelete
			}
			if pb.Document != nil {
				ch.Document = protoToStored(pb.Document)
			}
			select {
			case out <- ch:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

func (c *Client) createIndex(ctx context.Context, collection string, spec IndexSpec) error {
	_, err := c.stub.CreateIndex(c.rpcCtx(ctx), &t4docpb.CreateIndexRequest{Collection: collection, Index: indexToProto(spec)})
	return clientError(err)
}

func findRequest(collection string, filter Filter, opts findOptions) (*t4docpb.FindRequest, error) {
	pbFilter, err := filterToProto(filter)
	if err != nil {
		return nil, err
	}
	return &t4docpb.FindRequest{
		Collection: collection,
		Filter:     pbFilter,
		Limit:      int64(opts.limit),
		AllowScan:  opts.allowScan,
		MaxScan:    opts.maxScan,
	}, nil
}

func protoToStored(doc *t4docpb.Document) *storedDocument {
	if doc == nil {
		return nil
	}
	return &storedDocument{
		ID:             doc.Id,
		Raw:            doc.Json,
		Revision:       doc.Revision,
		CreateRevision: doc.CreateRevision,
	}
}

func clientError(err error) error {
	if err == nil {
		return nil
	}
	switch status.Code(err) {
	case codes.NotFound:
		return ErrNotFound
	case codes.Aborted:
		return ErrConflict
	case codes.FailedPrecondition:
		msg := status.Convert(err).Message()
		if msg == ErrScanLimitExceeded.Error() {
			return ErrScanLimitExceeded
		}
		return ErrMissingIndex
	case codes.InvalidArgument:
		return err
	default:
		return err
	}
}
