package t4doc

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/t4db/t4"
	t4etcd "github.com/t4db/t4/etcd"
	"github.com/t4db/t4/etcd/auth"
	"github.com/t4db/t4/pkg/object"
)

func TestStandaloneDocumentServiceSharesEtcdPort(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	addr, cleanup := startDocTestServer(t, nil, nil)
	defer cleanup()

	docClient, err := Dial(ctx, addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial doc: %v", err)
	}
	defer docClient.Close()
	users := NewCollection[map[string]any](docClient, "users")
	if _, err := users.Put(ctx, "alice", map[string]any{"email": "alice@example.com"}); err != nil {
		t.Fatalf("doc put: %v", err)
	}
	got, err := users.FindByID(ctx, "alice")
	if err != nil {
		t.Fatalf("doc get: %v", err)
	}
	if got.Value["email"] != "alice@example.com" {
		t.Fatalf("doc = %#v", got.Value)
	}

	conn, err := grpc.DialContext(ctx, addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial etcd: %v", err)
	}
	defer conn.Close()
	kv := etcdserverpb.NewKVClient(conn)
	if _, err := kv.Put(ctx, &etcdserverpb.PutRequest{Key: []byte("/kv/k"), Value: []byte("v")}); err != nil {
		t.Fatalf("etcd put: %v", err)
	}
	resp, err := kv.Range(ctx, &etcdserverpb.RangeRequest{Key: []byte("/kv/k")})
	if err != nil {
		t.Fatalf("etcd range: %v", err)
	}
	if len(resp.Kvs) != 1 || string(resp.Kvs[0].Value) != "v" {
		t.Fatalf("etcd kvs = %+v", resp.Kvs)
	}
}

func TestStandaloneDocumentAuth(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	node, err := t4.Open(t4.Config{DataDir: t.TempDir(), ObjectStore: object.NewMem()})
	if err != nil {
		t.Fatalf("open node: %v", err)
	}
	defer node.Close()
	store, err := auth.NewStore(node)
	if err != nil {
		t.Fatalf("auth store: %v", err)
	}
	tokens := auth.NewTokenStore(ctx, 5*time.Minute, nil)
	if err := store.PutRole(ctx, auth.Role{
		Name: "doc-users",
		Permissions: []auth.Permission{{
			Key:      auth.DocumentPermissionKey("users"),
			RangeEnd: "/doc/users0",
			PermType: auth.READWRITE,
		}},
	}); err != nil {
		t.Fatalf("put role: %v", err)
	}
	if err := store.PutUser(ctx, auth.User{Name: "alice"}, "secret"); err != nil {
		t.Fatalf("put user: %v", err)
	}
	if err := store.GrantRole(ctx, "alice", "doc-users"); err != nil {
		t.Fatalf("grant role: %v", err)
	}
	if err := store.PutUser(ctx, auth.User{Name: auth.RootUser}, "rootpass"); err != nil {
		t.Fatalf("put root: %v", err)
	}
	if err := store.Enable(ctx); err != nil {
		t.Fatalf("enable auth: %v", err)
	}

	addr, cleanup := startDocTestServer(t, store, tokens, node)
	defer cleanup()

	unauth, err := Dial(ctx, addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial unauth: %v", err)
	}
	defer unauth.Close()
	_, err = NewCollection[map[string]any](unauth, "users").Put(ctx, "alice", map[string]any{"ok": true})
	if err == nil {
		t.Fatal("expected unauthenticated document write to fail")
	}

	token, err := tokens.Generate("alice")
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	authed, err := Dial(ctx, addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial authed: %v", err)
	}
	defer authed.Close()
	authed.SetToken(token)
	if _, err := NewCollection[map[string]any](authed, "users").Put(ctx, "alice", map[string]any{"ok": true}); err != nil {
		t.Fatalf("authorized put: %v", err)
	}
	if _, err := NewCollection[map[string]any](authed, "other").Put(ctx, "x", map[string]any{"ok": true}); err == nil {
		t.Fatal("expected permission denied for other collection")
	}
}

func startDocTestServer(t *testing.T, store *auth.Store, tokens *auth.TokenStore, existing ...*t4.Node) (string, func()) {
	t.Helper()
	var node *t4.Node
	if len(existing) > 0 {
		node = existing[0]
	} else {
		var err error
		node, err = t4.Open(t4.Config{DataDir: t.TempDir(), ObjectStore: object.NewMem()})
		if err != nil {
			t.Fatalf("open node: %v", err)
		}
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer(t4etcd.NewServerOptions(store, tokens)...)
	t4etcd.New(node, store, tokens).Register(srv)
	docDB := Open(node)
	opts := []ServerOption{}
	if store != nil {
		opts = append(opts, WithAuthorizer(func(ctx context.Context, collection string, write bool) error {
			return auth.CheckDocumentPermission(ctx, store, tokens, collection, write)
		}))
	}
	NewServer(docDB, opts...).Register(srv)
	go srv.Serve(lis) //nolint:errcheck
	return lis.Addr().String(), func() {
		srv.Stop()
		docDB.Close()
		lis.Close()
		if len(existing) == 0 {
			node.Close()
		}
	}
}

func TestClientErrorMapping(t *testing.T) {
	if !errors.Is(clientError(rpcError(ErrNotFound)), ErrNotFound) {
		t.Fatal("not found mapping failed")
	}
}
