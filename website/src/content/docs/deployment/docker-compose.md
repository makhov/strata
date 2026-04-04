---
title: Docker Compose
description: Run Strata with Docker Compose — single node, MinIO-backed, and 3-node cluster examples.
---

## Single node, local only

The simplest setup — no S3, data on a named volume.

```yaml
# compose.yml
services:
  strata:
    image: ghcr.io/strata-db/strata:latest
    command: run --data-dir /var/lib/strata --listen 0.0.0.0:3379
    ports:
      - "3379:3379"
    volumes:
      - strata-data:/var/lib/strata

volumes:
  strata-data:
```

```bash
docker compose up -d
etcdctl --endpoints=localhost:3379 put /hello world
```

---

## Single node with MinIO (S3-compatible)

```yaml
# compose.yml
services:
  minio:
    image: minio/minio:latest
    command: server /data --console-address ":9001"
    environment:
      MINIO_ROOT_USER: minioadmin
      MINIO_ROOT_PASSWORD: minioadmin
    ports:
      - "9000:9000"
      - "9001:9001"
    volumes:
      - minio-data:/data
    healthcheck:
      test: ["CMD", "mc", "ready", "local"]
      interval: 5s
      retries: 5

  minio-init:
    image: minio/mc:latest
    depends_on:
      minio:
        condition: service_healthy
    entrypoint: >
      /bin/sh -c "
        mc alias set local http://minio:9000 minioadmin minioadmin &&
        mc mb --ignore-existing local/strata
      "

  strata:
    image: ghcr.io/strata-db/strata:latest
    command: >
      run
      --data-dir /var/lib/strata
      --listen 0.0.0.0:3379
      --s3-bucket strata
      --s3-prefix data/
      --s3-endpoint http://minio:9000
    environment:
      AWS_ACCESS_KEY_ID: minioadmin
      AWS_SECRET_ACCESS_KEY: minioadmin
      AWS_DEFAULT_REGION: us-east-1
    ports:
      - "3379:3379"
    volumes:
      - strata-data:/var/lib/strata
    depends_on:
      minio-init:
        condition: service_completed_successfully

volumes:
  minio-data:
  strata-data:
```

---

## 3-node cluster with MinIO

```yaml
# compose.yml
x-strata-common: &strata-common
  image: ghcr.io/strata-db/strata:latest
  environment:
    AWS_ACCESS_KEY_ID: minioadmin
    AWS_SECRET_ACCESS_KEY: minioadmin
    AWS_DEFAULT_REGION: us-east-1
  depends_on:
    minio-init:
      condition: service_completed_successfully

services:
  minio:
    image: minio/minio:latest
    command: server /data --console-address ":9001"
    environment:
      MINIO_ROOT_USER: minioadmin
      MINIO_ROOT_PASSWORD: minioadmin
    volumes:
      - minio-data:/data
    healthcheck:
      test: ["CMD", "mc", "ready", "local"]
      interval: 5s
      retries: 5

  minio-init:
    image: minio/mc:latest
    depends_on:
      minio:
        condition: service_healthy
    entrypoint: >
      /bin/sh -c "
        mc alias set local http://minio:9000 minioadmin minioadmin &&
        mc mb --ignore-existing local/strata
      "

  strata-0:
    <<: *strata-common
    command: >
      run
      --data-dir /var/lib/strata
      --listen 0.0.0.0:3379
      --s3-bucket strata
      --s3-prefix cluster/
      --s3-endpoint http://minio:9000
      --node-id strata-0
      --peer-listen 0.0.0.0:3380
      --advertise-peer strata-0:3380
      --metrics-addr 0.0.0.0:9090
    ports:
      - "3379:3379"
    volumes:
      - strata-0-data:/var/lib/strata

  strata-1:
    <<: *strata-common
    command: >
      run
      --data-dir /var/lib/strata
      --listen 0.0.0.0:3379
      --s3-bucket strata
      --s3-prefix cluster/
      --s3-endpoint http://minio:9000
      --node-id strata-1
      --peer-listen 0.0.0.0:3380
      --advertise-peer strata-1:3380
      --metrics-addr 0.0.0.0:9090
    ports:
      - "3380:3379"
    volumes:
      - strata-1-data:/var/lib/strata

  strata-2:
    <<: *strata-common
    command: >
      run
      --data-dir /var/lib/strata
      --listen 0.0.0.0:3379
      --s3-bucket strata
      --s3-prefix cluster/
      --s3-endpoint http://minio:9000
      --node-id strata-2
      --peer-listen 0.0.0.0:3380
      --advertise-peer strata-2:3380
      --metrics-addr 0.0.0.0:9090
    ports:
      - "3381:3379"
    volumes:
      - strata-2-data:/var/lib/strata

volumes:
  minio-data:
  strata-0-data:
  strata-1-data:
  strata-2-data:
```

```bash
docker compose up -d

# All three nodes are etcd-compatible; write to one, read from another.
etcdctl --endpoints=localhost:3379 put /hello world
etcdctl --endpoints=localhost:3380 get /hello
etcdctl --endpoints=localhost:3381 get /hello
```

---

## Build the image locally

```bash
git clone https://github.com/strata-db/strata
cd strata
docker build -t strata:local .
```

The Dockerfile produces a minimal distroless image (~10 MB):

```dockerfile
FROM golang:1.25-bookworm AS builder
# ... builds /strata binary with CGO_ENABLED=0

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /strata /strata
EXPOSE 3379 3380 9090
ENTRYPOINT ["/strata"]
CMD ["run"]
```

To use your local build, replace `image: ghcr.io/strata-db/strata:latest` with `image: strata:local` in the Compose files above.

---

## Health checks

```yaml
strata:
  # ...
  healthcheck:
    test: ["CMD-SHELL", "wget -qO- http://localhost:9090/healthz || exit 1"]
    interval: 10s
    timeout: 5s
    retries: 3
    start_period: 15s
```

- `/healthz` — returns 200 once the node has started
- `/readyz` — returns 200 when the node is ready to serve reads (after WAL replay and election)
