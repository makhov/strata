---
title: Migrating from etcd
description: How to move an existing etcd workload to Strata — standalone binary replacement and embedded library adoption.
---

Strata implements the full etcd v3 gRPC protocol. In most cases, replacing the etcd binary with `strata run` and pointing your existing clients at the new endpoint is all that's needed.

---

## Compatibility

Strata supports the following etcd v3 operations:

| etcd operation | Strata support |
|---|---|
| `Range` (Get / List / prefix scan) | Full |
| `Put` | Full |
| `DeleteRange` (single key or prefix) | Full |
| `Txn` (compare-and-set) | Full (MOD revision comparisons) |
| `Watch` | Full (history replay, cancel) |
| `Compact` | Full |
| `LeaseGrant` / `LeaseKeepAlive` / `LeaseRevoke` | Stubbed (TTL=60, no expiry eviction) |
| `AuthEnable` / Users / Roles | Full |
| `MemberList` | Returns single synthetic member |
| `Snapshot` | Not supported |

**Leases without expiry**: Strata's lease implementation stubs TTL renewal and does not evict keys when a lease expires. If your workload relies on lease expiry to automatically delete keys (e.g. ephemeral service registrations), you'll need to delete those keys explicitly on shutdown, or use `Watch` to detect disconnected clients.

---

## Migrating the standalone binary

### 1. Export data from etcd

```bash
# Snapshot the existing etcd cluster.
etcdctl --endpoints=http://etcd:2379 snapshot save etcd-snapshot.db
```

Alternatively, iterate and re-write all keys after cutover — suitable for small datasets.

### 2. Start Strata

```bash
# Single node with S3
strata run \
  --data-dir /var/lib/strata \
  --listen 0.0.0.0:3379 \
  --s3-bucket my-bucket \
  --s3-prefix strata/
```

### 3. Import data

Strata does not support etcd snapshot restore directly. Replay keys using `etcdctl` or a migration script:

```bash
# Export all keys from etcd as key-value pairs
etcdctl --endpoints=http://etcd:2379 get / --prefix --print-value-only=false \
  | awk 'NR%2==1{key=$0} NR%2==0{print key, $0}' > keys.txt

# Write them to Strata
while read -r key value; do
  etcdctl --endpoints=http://strata:3379 put "$key" "$value"
done < keys.txt
```

For large datasets, write a short Go program using the etcd client library to stream and replay:

```go
import (
    clientv3 "go.etcd.io/etcd/client/v3"
)

func migrate(ctx context.Context, src, dst *clientv3.Client) error {
    resp, err := src.Get(ctx, "/", clientv3.WithPrefix())
    if err != nil {
        return err
    }
    for _, kv := range resp.Kvs {
        if _, err := dst.Put(ctx, string(kv.Key), string(kv.Value)); err != nil {
            return err
        }
    }
    return nil
}
```

### 4. Update client endpoints

Change your application's etcd endpoint from `http://etcd:2379` to `http://strata:3379`. No client code changes needed — the etcd v3 Go client, Java client, Python client, and `etcdctl` all work against Strata unchanged.

---

## Migrating embedded etcd (e.g. k3s / kine)

If you're using etcd embedded in Kubernetes (k3s, k0s) via kine or a direct etcd embed, consider switching to Strata's standalone binary as an etcd-compatible backend:

```bash
# k3s example: point the datastore at Strata
k3s server --datastore-endpoint=http://strata:3379
```

Or deploy Strata on the cluster itself and point the control plane at its ClusterIP Service.

---

## Adopting the embedded library

If you're currently running the etcd server as a sidecar and connecting via the etcd Go client, you can replace both with the embedded Strata library — eliminating the sidecar process entirely.

### Before (etcd sidecar + etcd client)

```go
// Connecting to a separate etcd sidecar process
cli, err := clientv3.New(clientv3.Config{
    Endpoints: []string{"localhost:2379"},
})

resp, err := cli.Get(ctx, "/config/timeout")
value := string(resp.Kvs[0].Value)

_, err = cli.Put(ctx, "/config/timeout", "30s")
```

### After (embedded Strata)

```go
import "github.com/strata-db/strata"

// Embedded — no separate process
node, err := strata.Open(strata.Config{
    DataDir:     "/var/lib/myapp/strata",
    ObjectStore: s3Store, // same S3 durability you had before
})

kv, err := node.Get("/config/timeout")
value := string(kv.Value)

_, err = node.Put(ctx, "/config/timeout", []byte("30s"), 0)
```

Key API differences from the etcd v3 Go client:

| etcd client | Strata embedded |
|---|---|
| `cli.Get(ctx, key)` | `node.Get(key)` (no ctx; reads are local) |
| `cli.Get(ctx, prefix, WithPrefix())` | `node.List(prefix)` |
| `cli.Put(ctx, key, value)` | `node.Put(ctx, key, []byte(value), 0)` |
| `cli.Delete(ctx, key)` | `node.Delete(ctx, key)` |
| `cli.Watch(ctx, prefix, WithPrefix())` | `node.Watch(ctx, prefix, 0)` |
| `cli.Txn(ctx).If(...).Then(Put).Commit()` | `node.Create` / `node.Update` / `node.DeleteIfRevision` |
| `cli.Grant(ctx, ttl)` + lease ID on Put | Leases are stubbed; use `Delete` on shutdown instead |

---

## Feature gaps to plan for

| etcd feature | Strata behaviour | Workaround |
|---|---|---|
| Lease expiry / TTL eviction | Not implemented | Delete keys explicitly on shutdown; use `Watch` to detect disconnects |
| `etcdctl snapshot restore` | Not supported | Use `strata branch fork` for point-in-time copies |
| etcd cluster membership API | Single synthetic member | Not needed for standard clients |
| etcd v2 API | Not supported | Migrate to v3 first |
| gRPC gateway (HTTP/JSON) | Not included | Use a gRPC proxy (e.g. grpc-gateway) in front |
