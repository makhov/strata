# TLA+ Models

T4 uses TLA+ for small protocol models where concurrency, stale state, and
failure interleavings are easier to explore with model checking than with
ordinary tests.

Run all checked models with:

```sh
make tla
```

The runner uses the first available option:

1. `tlc` from `PATH`
2. `TLA2TOOLS_JAR=/path/to/tla2tools.jar`
3. the pinned jar downloaded by `hack/download-tla.sh`

The pinned TLC version is kept in `hack/download-tla.sh`, along with the jar's
SHA-256. CI runs the same `make tla` target.

Generated TLC state directories are ignored by git.
