---
title: Security
description: Securing Strata — TLS setup, mTLS between peers, client authentication, and RBAC.
---

## Overview

Strata has two independently configurable TLS surfaces:

| Surface | Flag prefix | What it protects |
|---|---|---|
| **Client TLS** | `--client-tls-*` | etcd gRPC port (3379) — traffic between your application and Strata |
| **Peer mTLS** | `--peer-tls-*` | WAL replication port (3380) — traffic between Strata nodes |

Both use standard PEM-encoded certificates. You can enable either or both independently.

---

## Generating test certificates

For development, use `openssl` to create a self-signed CA and certificates:

```bash
# CA key and certificate
openssl genrsa -out ca.key 4096
openssl req -new -x509 -days 3650 -key ca.key -out ca.crt \
  -subj "/CN=strata-ca"

# Server key and CSR
openssl genrsa -out server.key 4096
openssl req -new -key server.key -out server.csr \
  -subj "/CN=strata-server"

# Sign with CA, include SANs
openssl x509 -req -days 3650 -in server.csr -CA ca.crt -CAkey ca.key \
  -CAcreateserial -out server.crt \
  -extfile <(printf "subjectAltName=DNS:localhost,IP:127.0.0.1")

echo "Files: ca.crt, server.crt, server.key"
```

For mTLS (peer-to-peer), generate a second cert for each node, or use a single shared cert if all nodes are behind the same CA.

---

## Client TLS

### Server-only TLS (encryption, no client cert required)

Clients connect with TLS but aren't required to present a certificate. Use this when your clients support TLS but not mTLS.

```bash
strata run \
  --data-dir /var/lib/strata \
  --listen 0.0.0.0:3379 \
  --client-tls-cert /etc/strata/tls/server.crt \
  --client-tls-key  /etc/strata/tls/server.key
```

Clients:

```bash
etcdctl --endpoints=https://strata:3379 \
        --cacert /etc/strata/tls/ca.crt \
        put /hello world
```

Go client:

```go
tlsCfg, err := tlsconfig.ClientConfig(tlsconfig.Options{
    CAFile: "/etc/strata/tls/ca.crt",
})
cli, err := clientv3.New(clientv3.Config{
    Endpoints: []string{"https://strata:3379"},
    TLS:       tlsCfg,
})
```

### Mutual TLS (mTLS — client cert required)

Add `--client-tls-ca` to require clients to present a certificate signed by the given CA:

```bash
strata run \
  --data-dir /var/lib/strata \
  --listen 0.0.0.0:3379 \
  --client-tls-cert /etc/strata/tls/server.crt \
  --client-tls-key  /etc/strata/tls/server.key \
  --client-tls-ca   /etc/strata/tls/ca.crt
```

Generate a client certificate:

```bash
openssl genrsa -out client.key 4096
openssl req -new -key client.key -out client.csr -subj "/CN=my-app"
openssl x509 -req -days 365 -in client.csr -CA ca.crt -CAkey ca.key \
  -CAcreateserial -out client.crt
```

Connect with client cert:

```bash
etcdctl --endpoints=https://strata:3379 \
        --cacert  /etc/strata/tls/ca.crt \
        --cert    /etc/strata/tls/client.crt \
        --key     /etc/strata/tls/client.key \
        put /hello world
```

### Embedded library

Pass `grpc.DialOption` credentials to the etcd client if you're using the etcd-compatible interface, or configure TLS on the gRPC connection directly. For the embedded `*strata.Node`, TLS applies only to the peer port — client reads go directly in-process without any network.

---

## Peer mTLS

Peer mTLS encrypts and authenticates WAL replication streams between nodes. All nodes must use the same CA.

```bash
strata run \
  --data-dir       /var/lib/strata \
  --peer-listen    0.0.0.0:3380 \
  --advertise-peer node-a.internal:3380 \
  --peer-tls-ca    /etc/strata/tls/ca.crt \
  --peer-tls-cert  /etc/strata/tls/node.crt \
  --peer-tls-key   /etc/strata/tls/node.key
```

The same cert/key pair can be used on all nodes if the cert includes all peer DNS names in its SANs:

```bash
openssl x509 -req -days 3650 -in node.csr -CA ca.crt -CAkey ca.key \
  -CAcreateserial -out node.crt \
  -extfile <(printf "subjectAltName=DNS:node-a.internal,DNS:node-b.internal,DNS:node-c.internal")
```

Or use separate certs per node — all must be signed by the same CA.

### Embedded library

```go
import "google.golang.org/grpc/credentials"

serverCreds, err := credentials.NewServerTLSFromFile(certFile, keyFile)
clientCreds, err := credentials.NewClientTLSFromFile(caFile, "")

node, err := strata.Open(strata.Config{
    PeerServerTLS: serverCreds,
    PeerClientTLS: clientCreds,
})
```

For mTLS with client cert verification, build `tls.Config` manually:

```go
cert, _ := tls.LoadX509KeyPair(certFile, keyFile)
caCert, _ := os.ReadFile(caFile)
pool := x509.NewCertPool()
pool.AppendCertsFromPEM(caCert)

serverTLS := &tls.Config{
    Certificates: []tls.Certificate{cert},
    ClientCAs:    pool,
    ClientAuth:   tls.RequireAndVerifyClientCert,
}
clientTLS := &tls.Config{
    Certificates: []tls.Certificate{cert},
    RootCAs:      pool,
}

node, err := strata.Open(strata.Config{
    PeerServerTLS: credentials.NewTLS(serverTLS),
    PeerClientTLS: credentials.NewTLS(clientTLS),
})
```

---

## cert-manager (Kubernetes)

Generate peer certificates automatically with cert-manager:

```yaml
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: strata-ca-issuer
spec:
  selfSigned: {}
---
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: strata-ca
  namespace: default
spec:
  isCA: true
  secretName: strata-ca-secret
  commonName: strata-ca
  issuerRef:
    name: strata-ca-issuer
    kind: ClusterIssuer
---
apiVersion: cert-manager.io/v1
kind: Issuer
metadata:
  name: strata-issuer
  namespace: default
spec:
  ca:
    secretName: strata-ca-secret
---
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: strata-peer-tls
  namespace: default
spec:
  secretName: strata-peer-tls
  issuerRef:
    name: strata-issuer
  dnsNames:
    - strata-0.strata-headless.default.svc.cluster.local
    - strata-1.strata-headless.default.svc.cluster.local
    - strata-2.strata-headless.default.svc.cluster.local
    - strata-headless.default.svc.cluster.local
  usages:
    - server auth
    - client auth
  duration: 8760h    # 1 year
  renewBefore: 720h  # renew 30 days before expiry
```

Then pass the secret to the Helm chart:

```bash
helm install strata oci://ghcr.io/strata-db/charts/strata \
  --set tls.peer.enabled=true \
  --set tls.peer.secretName=strata-peer-tls
```

---

## Authentication and RBAC

Strata implements the etcd v3 Auth API: username/password auth with bearer tokens and role-based access control.

### Enable auth

Auth requires a `root` user to exist before it can be enabled:

```bash
etcdctl --endpoints=localhost:3379 user add root
# Enter password at prompt

etcdctl --endpoints=localhost:3379 auth enable
```

Once enabled, all KV and Watch requests require authentication.

### Create users and roles

```bash
# Create a read-only role for /config/
etcdctl --endpoints=localhost:3379 --user root:pass \
  role add config-reader

etcdctl --endpoints=localhost:3379 --user root:pass \
  role grant-permission config-reader read /config/ --prefix

# Create a user and assign the role
etcdctl --endpoints=localhost:3379 --user root:pass \
  user add alice

etcdctl --endpoints=localhost:3379 --user root:pass \
  user grant-role alice config-reader
```

### RBAC rules

A request is allowed when the user has at least one role whose permissions cover the key and operation:

| Operation | Required permission |
|---|---|
| `Get` / `List` / `Watch` | `read` |
| `Put` / `Delete` / `Txn` | `write` |

Permission scopes:
- **Exact key**: matches a single key
- **Prefix** (`--prefix`): matches all keys starting with the prefix
- **Open-ended range** (`rangeEnd="\x00"`): matches all keys ≥ the start key

The `root` role bypasses all permission checks.

### Token TTL

Bearer tokens expire after `--token-ttl` seconds (default 300). The etcd Go client handles token refresh automatically when `--user` is provided.

```bash
strata run ... --auth-enabled --token-ttl 3600
```

---

## S3 bucket security

S3 is used for WAL segments, checkpoints, and the leader lock. The IAM policy for Strata's S3 access needs:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": [
        "s3:GetObject",
        "s3:PutObject",
        "s3:DeleteObject",
        "s3:ListBucket",
        "s3:HeadObject"
      ],
      "Resource": [
        "arn:aws:s3:::my-bucket",
        "arn:aws:s3:::my-bucket/strata/*"
      ]
    }
  ]
}
```

For leader election, Strata uses conditional PUTs (`If-None-Match`, `If-Match`). These are standard S3 operations and don't require additional permissions.

**Recommendations:**
- Use IRSA / Workload Identity — no static credentials in environment variables or Secrets
- Enable S3 bucket versioning if you use point-in-time restore
- Enable S3 server-side encryption (SSE-S3 or SSE-KMS)
- Restrict bucket access with a bucket policy that denies public access
