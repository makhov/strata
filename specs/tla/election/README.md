# TLA+ Election Model

This is the first formal model for T4. It focuses on the S3-backed leader lock:

- startup acquisition
- liveness touch
- takeover after a stale lock
- committed revision fencing
- leader stepdown

The model intentionally stays small. It treats S3 conditional writes as atomic
state transitions and abstracts away Pebble, gRPC, WAL segment upload, and
checkpoint restore. Those deserve separate models once this one is useful.

Run it with:

```sh
make tla
```

The runner accepts a `tlc` executable on `PATH`, a local TLA+ tools jar, or it
will download the project-pinned jar into `.cache/tla/`:

```sh
TLA2TOOLS_JAR=/path/to/tla2tools.jar make tla
```

The bounded default config uses three nodes, revisions `0..2`, terms `0..4`,
and a logical clock `0..4`. Increase those bounds only when investigating a
specific protocol change; small models are much easier to reason about and fast
enough for CI.
