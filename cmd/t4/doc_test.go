package main

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"

	"github.com/t4db/t4"
	"github.com/t4db/t4/pkg/object"
	"github.com/t4db/t4/t4doc"
)

func TestDocCLISmoke(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	addr, cleanup := startDocCLITestServer(t)
	defer cleanup()

	runDocCLI(t, ctx, addr, "put", "users", "alice", "--json", `{"email":"alice@example.com","status":"inactive"}`)
	got := runDocCLI(t, ctx, addr, "get", "users", "alice")
	if !strings.Contains(got, "alice@example.com") {
		t.Fatalf("doc get output missing email: %s", got)
	}

	runDocCLI(t, ctx, addr, "patch", "users", "alice", "--json", `{"status":"active"}`)
	runDocCLI(t, ctx, addr, "index", "create", "users", "status", "--field", "status", "--type", "string")
	found := runDocCLI(t, ctx, addr, "find", "users", "--eq", "status=active", "--type", "string", "--limit", "10")
	if !strings.Contains(found, "alice@example.com") {
		t.Fatalf("doc find output missing alice: %s", found)
	}

	watchCtx, stopWatch := context.WithCancel(ctx)
	defer stopWatch()
	watchOut := &safeBuffer{}
	watchDone := make(chan error, 1)
	watchCmd := rootCmd()
	watchCmd.SetOut(watchOut)
	watchCmd.SetErr(watchOut)
	watchCmd.SetArgs([]string{"doc", "--endpoint", addr, "watch", "users", "--eq", "status=active", "--type", "string"})
	go func() {
		watchDone <- watchCmd.ExecuteContext(watchCtx)
	}()

	for i := 0; ; i++ {
		id := fmt.Sprintf("watched-%d", i)
		runDocCLI(t, ctx, addr, "put", "users", id, "--json", fmt.Sprintf(`{"status":"active","seq":%d}`, i))
		if strings.Contains(watchOut.String(), id) {
			break
		}
		if i >= 20 {
			t.Fatalf("watch output never saw event: %s", watchOut.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
	stopWatch()
	select {
	case err := <-watchDone:
		if err != nil {
			t.Fatalf("doc watch: %v\noutput=%s", err, watchOut.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("doc watch did not stop after cancellation; output=%s", watchOut.String())
	}

	runDocCLI(t, ctx, addr, "delete", "users", "alice")
}

func runDocCLI(t *testing.T, ctx context.Context, addr string, args ...string) string {
	t.Helper()
	out := &bytes.Buffer{}
	cmd := rootCmd()
	cmd.SetOut(out)
	cmd.SetErr(out)
	cmd.SetArgs(append([]string{"doc", "--endpoint", addr}, args...))
	if err := cmd.ExecuteContext(ctx); err != nil {
		t.Fatalf("t4 doc %s: %v\noutput=%s", strings.Join(args, " "), err, out.String())
	}
	return out.String()
}

func startDocCLITestServer(t *testing.T) (string, func()) {
	t.Helper()
	node, err := t4.Open(t4.Config{DataDir: t.TempDir(), ObjectStore: object.NewMem()})
	if err != nil {
		t.Fatalf("open node: %v", err)
	}
	docDB := t4doc.Open(node)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer()
	t4doc.NewServer(docDB).Register(srv)
	go srv.Serve(lis) //nolint:errcheck
	return lis.Addr().String(), func() {
		srv.Stop()
		lis.Close()
		docDB.Close()
		node.Close()
	}
}

type safeBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}
