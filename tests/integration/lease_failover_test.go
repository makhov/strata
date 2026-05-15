package integration_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// Lease behavior across leader failover. The v1 contract:
//
//   1. A lease keepalive stream is per-leader. When the leader changes, the
//      existing stream fails cleanly; a fresh stream against the new leader
//      keeps the lease alive without recreating it.
//   2. An expired lease and all its attached keys are revoked durably across
//      failover. A new leader must not resurrect an expired lease, and the
//      lease loop on the new leader must reap the record + attached keys.
//   3. A revoke that crashes mid-flight on the old leader converges on the
//      new leader: all attached keys end up deleted and the lease record is
//      gone, regardless of where the crash landed.

func newClientForEndpoint(t *testing.T, endpoint string) *clientv3.Client {
	t.Helper()
	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   []string{endpoint},
		DialTimeout: 5 * time.Second,
		DialOptions: []grpc.DialOption{
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		},
	})
	if err != nil {
		t.Fatalf("etcd client (%s): %v", endpoint, err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	return cli
}

// TestLeaseKeepAliveAcrossLeaderChange asserts a keepalive stream against the
// new leader can hold a lease that was granted on the old leader. The old
// leader's stream is expected to fail; the v1 contract requires the lease
// itself to survive on the new leader.
func TestLeaseKeepAliveAcrossLeaderChange(t *testing.T) {
	cluster := newFailoverCluster(t, 3)
	leaderIdx := cluster.leaderIdx(t, 15*time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Grant the lease on the old leader.
	oldCli := newClientForEndpoint(t, cluster.endpoints[leaderIdx])
	grant, err := oldCli.Grant(ctx, 10)
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	leaseID := grant.ID

	// Send a few keepalives on the old stream so the lease is clearly being
	// renewed before failover.
	for i := 0; i < 3; i++ {
		if _, err := oldCli.KeepAliveOnce(ctx, leaseID); err != nil {
			t.Fatalf("keepalive %d (old leader): %v", i, err)
		}
	}

	// Wait for followers to replicate the grant so a promoted follower
	// already has the lease record.
	if err := cluster.nodes[leaderIdx].WaitForRevision(ctx, cluster.nodes[leaderIdx].CurrentRevision()); err != nil {
		t.Fatalf("WaitForRevision: %v", err)
	}
	for i, n := range cluster.nodes {
		if i == leaderIdx {
			continue
		}
		if err := n.WaitForRevision(ctx, cluster.nodes[leaderIdx].CurrentRevision()); err != nil {
			t.Fatalf("node-%d WaitForRevision: %v", i, err)
		}
	}

	// Graceful failover: stop the leader's etcd server then close the Node.
	cluster.servers[leaderIdx].Stop()
	cluster.servers[leaderIdx] = nil
	_ = cluster.nodes[leaderIdx].Close()
	cluster.nodes[leaderIdx] = nil

	newIdx := cluster.waitNewLeader(t, leaderIdx, 30*time.Second)
	t.Logf("new leader: node-%d (endpoint=%s)", newIdx, cluster.endpoints[newIdx])

	newCli := newClientForEndpoint(t, cluster.endpoints[newIdx])

	// The lease must still exist on the new leader.
	ttlResp, err := newCli.TimeToLive(ctx, leaseID)
	if err != nil {
		t.Fatalf("TimeToLive after failover: %v", err)
	}
	if ttlResp.TTL <= 0 {
		t.Fatalf("lease %d expired before failover completed: TTL=%d", leaseID, ttlResp.TTL)
	}

	// Renew via a fresh keepalive stream against the new leader.
	for i := 0; i < 3; i++ {
		if _, err := newCli.KeepAliveOnce(ctx, leaseID); err != nil {
			t.Fatalf("keepalive %d (new leader): %v", i, err)
		}
	}

	// Still alive after the renewals.
	ttlResp, err = newCli.TimeToLive(ctx, leaseID)
	if err != nil {
		t.Fatalf("TimeToLive after new-leader keepalives: %v", err)
	}
	if ttlResp.TTL < 5 {
		t.Errorf("new-leader keepalive did not refresh TTL: got %d, want ≥ 5", ttlResp.TTL)
	}
}

// TestLeaseExpiryDurableAcrossFailover asserts that an expired lease and its
// attached key are revoked durably across failover. The new leader's lease
// loop must reap the lease record and delete its attached keys; the lease
// must not be resurrected by WAL replay.
func TestLeaseExpiryDurableAcrossFailover(t *testing.T) {
	cluster := newFailoverCluster(t, 3)
	leaderIdx := cluster.leaderIdx(t, 15*time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cli := newClientForEndpoint(t, cluster.endpoints[leaderIdx])

	grant, err := cli.Grant(ctx, 2) // 2s TTL
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	leaseID := grant.ID

	const attachedKey = "/lease-failover/expiry-bound"
	if _, err := cli.Put(ctx, attachedKey, "v", clientv3.WithLease(leaseID)); err != nil {
		t.Fatalf("put with lease: %v", err)
	}

	// Wait for followers to have the lease + attached key.
	leaderRev := cluster.nodes[leaderIdx].CurrentRevision()
	for i, n := range cluster.nodes {
		if i == leaderIdx {
			continue
		}
		if err := n.WaitForRevision(ctx, leaderRev); err != nil {
			t.Fatalf("node-%d WaitForRevision: %v", i, err)
		}
	}

	// Graceful failover.
	cluster.servers[leaderIdx].Stop()
	cluster.servers[leaderIdx] = nil
	_ = cluster.nodes[leaderIdx].Close()
	cluster.nodes[leaderIdx] = nil

	newIdx := cluster.waitNewLeader(t, leaderIdx, 30*time.Second)
	t.Logf("new leader: node-%d", newIdx)

	newCli := newClientForEndpoint(t, cluster.endpoints[newIdx])

	// Wait past TTL + lease-loop tick (1s) + some slack.
	deadline := time.Now().Add(10 * time.Second)
	keyGone := false
	for time.Now().Before(deadline) {
		resp, err := newCli.Get(ctx, attachedKey)
		if err != nil {
			t.Fatalf("Get attached key: %v", err)
		}
		if resp.Count == 0 {
			keyGone = true
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !keyGone {
		t.Fatalf("attached key %s never deleted after lease expiry across failover", attachedKey)
	}

	// Lease record must be gone — NotFound is the etcd-style "lease no
	// longer exists" reply; TTL <= 0 means the wall-clock has passed.
	if ttlResp, err := newCli.TimeToLive(ctx, leaseID); err == nil {
		if ttlResp.TTL > 0 {
			t.Errorf("lease %d still alive after expiry: TTL=%d", leaseID, ttlResp.TTL)
		}
	} else if status.Code(err) != codes.NotFound {
		t.Fatalf("TimeToLive after expiry: %v", err)
	}
}

// TestLeasePartialRevokeRecoveredAfterFailover asserts that a revoke crashing
// mid-flight on the old leader converges on the new leader: all attached
// keys are deleted and the lease record is gone, regardless of where the
// crash landed.
//
// We trigger the race-style crash by issuing LeaseRevoke and immediately
// closing the leader in parallel. Across N iterations the kill lands at
// different points within the per-key delete loop; the final convergence
// assertion must hold every time.
func TestLeasePartialRevokeRecoveredAfterFailover(t *testing.T) {
	const iterations = 3

	for iter := 0; iter < iterations; iter++ {
		t.Run(fmt.Sprintf("iter%d", iter), func(t *testing.T) {
			cluster := newFailoverCluster(t, 3)
			leaderIdx := cluster.leaderIdx(t, 15*time.Second)

			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			cli := newClientForEndpoint(t, cluster.endpoints[leaderIdx])

			grant, err := cli.Grant(ctx, 5)
			if err != nil {
				t.Fatalf("grant: %v", err)
			}
			leaseID := grant.ID

			const n = 5
			keys := make([]string, n)
			for i := 0; i < n; i++ {
				keys[i] = fmt.Sprintf("/lease-failover/partial-%d-%d", iter, i)
				if _, err := cli.Put(ctx, keys[i], "v", clientv3.WithLease(leaseID)); err != nil {
					t.Fatalf("put %s with lease: %v", keys[i], err)
				}
			}

			leaderRev := cluster.nodes[leaderIdx].CurrentRevision()
			for i, nd := range cluster.nodes {
				if i == leaderIdx {
					continue
				}
				if err := nd.WaitForRevision(ctx, leaderRev); err != nil {
					t.Fatalf("node-%d WaitForRevision: %v", i, err)
				}
			}

			// Issue revoke and crash the leader concurrently. The revoke
			// may complete fully, partially, or not at all on the old
			// leader depending on timing.
			revokeCtx, revokeCancel := context.WithTimeout(ctx, 30*time.Second)
			revokeDone := make(chan error, 1)
			go func() {
				_, err := cli.Revoke(revokeCtx, leaseID)
				revokeDone <- err
			}()

			// Tiny delay so the revoke RPC has a chance to land before
			// the crash. Empirically lands in the middle of the
			// per-key delete loop on a varying number of iterations.
			time.Sleep(1 * time.Millisecond)
			cluster.servers[leaderIdx].Stop()
			cluster.servers[leaderIdx] = nil
			_ = cluster.nodes[leaderIdx].Close()
			cluster.nodes[leaderIdx] = nil

			// Whatever happens to the revoke RPC, drain its result so
			// the goroutine doesn't leak.
			select {
			case rErr := <-revokeDone:
				if rErr != nil && !isExpectedRevokeErr(rErr) {
					t.Logf("revoke returned: %v", rErr)
				}
			case <-time.After(15 * time.Second):
				revokeCancel()
				t.Logf("revoke RPC did not return; forcing cancellation")
			}
			revokeCancel()

			newIdx := cluster.waitNewLeader(t, leaderIdx, 30*time.Second)
			t.Logf("iter %d new leader: node-%d", iter, newIdx)
			newCli := newClientForEndpoint(t, cluster.endpoints[newIdx])

			// Convergence: every attached key must end up deleted, and
			// the lease must be reported as gone (TimeToLive NotFound or
			// TTL <= 0). The lease loop on the new leader reaps expired
			// records on a 1s tick; the 5s TTL we set means the test
			// must converge within ~10s even when the revoke RPC never
			// commits.
			deadline := time.Now().Add(20 * time.Second)
			for {
				allGone := true
				for _, k := range keys {
					resp, err := newCli.Get(ctx, k)
					if err != nil {
						t.Fatalf("Get %s: %v", k, err)
					}
					if resp.Count != 0 {
						allGone = false
						break
					}
				}
				ttlGone := false
				if ttlResp, err := newCli.TimeToLive(ctx, leaseID); err == nil {
					ttlGone = ttlResp.TTL <= 0
				} else if status.Code(err) == codes.NotFound {
					ttlGone = true
				} else {
					t.Fatalf("TimeToLive: %v", err)
				}
				if allGone && ttlGone {
					return
				}
				if time.Now().After(deadline) {
					for _, k := range keys {
						resp, _ := newCli.Get(ctx, k)
						t.Logf("final key %s: count=%d", k, resp.Count)
					}
					t.Fatalf("did not converge: ttlGone=%v allGone=%v", ttlGone, allGone)
				}
				time.Sleep(200 * time.Millisecond)
			}
		})
	}
}

func isExpectedRevokeErr(err error) bool {
	if err == nil {
		return true
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	c := status.Code(err)
	return c == codes.Unavailable || c == codes.Canceled || c == codes.DeadlineExceeded
}
