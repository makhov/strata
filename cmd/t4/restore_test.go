package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"

	"github.com/t4db/t4"
	"github.com/t4db/t4/internal/checkpoint"
	"github.com/t4db/t4/pkg/object"
)

const (
	restoreTestBucket    = "t4-cli-restore"
	restoreTestAccessKey = "access"
	restoreTestSecretKey = "secret"
)

func TestRestoreCheckpointFromS3Smoke(t *testing.T) {
	ctx := context.Background()
	endpoint := newRestoreFakeS3(t, ctx)
	prefix := "smoke"

	store, err := object.NewS3StoreFromConfig(ctx, object.S3Config{
		Bucket:          restoreTestBucket,
		Prefix:          prefix,
		Endpoint:        endpoint,
		AccessKeyID:     restoreTestAccessKey,
		SecretAccessKey: restoreTestSecretKey,
	})
	if err != nil {
		t.Fatalf("new source S3 store: %v", err)
	}

	source, err := t4.Open(t4.Config{
		DataDir:            t.TempDir(),
		ObjectStore:        store,
		CheckpointInterval: 25 * time.Millisecond,
		CheckpointEntries:  1,
		SegmentMaxAge:      25 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("open source node: %v", err)
	}

	var lastRev int64
	for i := range 3 {
		lastRev, err = source.Put(ctx, fmt.Sprintf("/restore/%d", i), []byte(fmt.Sprintf("value-%d", i)), 0)
		if err != nil {
			t.Fatalf("put source key %d: %v", i, err)
		}
	}
	waitForCheckpointAtLeast(t, ctx, store, lastRev)
	if err := source.Close(); err != nil {
		t.Fatalf("close source node: %v", err)
	}

	listOut := &bytes.Buffer{}
	listCmd := rootCmd()
	listCmd.SetOut(listOut)
	listCmd.SetErr(listOut)
	listCmd.SetArgs(append([]string{"restore", "list"}, restoreS3Args(endpoint, prefix)...))
	if err := listCmd.Execute(); err != nil {
		t.Fatalf("restore list: %v\noutput:\n%s", err, listOut.String())
	}
	if out := listOut.String(); !strings.Contains(out, "CHECKPOINT") || !strings.Contains(out, "(latest)") {
		t.Fatalf("expected listed latest checkpoint, got:\n%s", out)
	}

	restoreDir := t.TempDir()
	restoreOut := &bytes.Buffer{}
	restoreCmd := rootCmd()
	restoreCmd.SetOut(restoreOut)
	restoreCmd.SetErr(restoreOut)
	restoreCmd.SetArgs(append([]string{"restore", "checkpoint", "--data-dir", restoreDir}, restoreS3Args(endpoint, prefix)...))
	if err := restoreCmd.Execute(); err != nil {
		t.Fatalf("restore checkpoint: %v\noutput:\n%s", err, restoreOut.String())
	}
	if out := restoreOut.String(); !strings.Contains(out, "Restored checkpoint") || !strings.Contains(out, fmt.Sprintf("revision:  %d", lastRev)) {
		t.Fatalf("expected restore summary for revision %d, got:\n%s", lastRev, out)
	}

	countOut := &bytes.Buffer{}
	countCmd := rootCmd()
	countCmd.SetOut(countOut)
	countCmd.SetErr(countOut)
	countCmd.SetArgs([]string{"inspect", "count", "--data-dir", restoreDir, "--prefix", "/restore/"})
	if err := countCmd.Execute(); err != nil {
		t.Fatalf("inspect restored count: %v\noutput:\n%s", err, countOut.String())
	}
	if got := strings.TrimSpace(countOut.String()); got != "/restore/: 3" {
		t.Fatalf("unexpected restored count output: %q", got)
	}
}

func newRestoreFakeS3(t *testing.T, ctx context.Context) string {
	t.Helper()

	faker := gofakes3.New(s3mem.New())
	server := httptest.NewServer(faker.Server())
	t.Cleanup(server.Close)

	cfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(restoreTestAccessKey, restoreTestSecretKey, ""),
		),
		awsconfig.WithRegion("us-east-1"),
		awsconfig.WithBaseEndpoint(server.URL),
	)
	if err != nil {
		t.Fatalf("aws config: %v", err)
	}
	client := s3.NewFromConfig(cfg, func(o *s3.Options) { o.UsePathStyle = true })
	if _, err := client.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: aws.String(restoreTestBucket),
	}); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	return server.URL
}

func restoreS3Args(endpoint, prefix string) []string {
	return []string{
		"--s3-bucket", restoreTestBucket,
		"--s3-prefix", prefix,
		"--s3-endpoint", endpoint,
		"--s3-access-key-id", restoreTestAccessKey,
		"--s3-secret-access-key", restoreTestSecretKey,
	}
}

func waitForCheckpointAtLeast(t *testing.T, ctx context.Context, store object.Store, minRevision int64) {
	t.Helper()

	cp := checkpoint.New(nil)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		manifest, err := cp.ReadManifest(ctx, store)
		if err != nil {
			t.Fatalf("read checkpoint manifest: %v", err)
		}
		if manifest != nil && manifest.Revision >= minRevision {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for checkpoint at revision >= %d", minRevision)
}
