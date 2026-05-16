# Releasing T4

What a maintainer needs to know that is **not** already encoded in CI workflows or branch-protection settings. For everything else, treat those configs as the source of truth.

## What gates a release

The required CI checks for a release tag are whichever checks are marked **required** in this repo's branch-protection settings on `main`. That config is authoritative — this page does not duplicate the list.

In addition, two nightly checks must be green within 7 days of the release SHA:

- `nightly.yml` → `jepsen` — full nemesis matrix; guards linearizability.
- `nightly.yml` → `stress` — `TestLongRunningConsistency`, 30 min.

If the most recent nightly is stale, trigger one on the release SHA before tagging:

```bash
gh workflow run nightly.yml --ref <release-sha>
```

## Cutting the release

1. Audit the v1 [compatibility contract](v1-compatibility.md) against the previous tag: `t4.Config` defaults, exported types, object-store layout, WAL frames. Any break needs a major version bump.
2. Update `CHANGELOG.md`.
3. Tag and push:
   ```bash
   git tag -a vX.Y.Z -m "vX.Y.Z" <release-sha>
   git push origin vX.Y.Z
   ```
4. `image-release.yml` fires automatically: draft release, multi-arch container image to GHCR, four binaries, `checksums.txt`.
5. Edit the draft release notes. **Cite the last green nightly Jepsen run** (link to the workflow run). Then unpublish "Draft".

The Helm chart is released on a separate `chart/vA.B.C` tag (`chart-release.yml`). Only push it when the chart actually changed since the last chart tag.

## Rollback policy

If a regression slips out, **move forward, never rewrite**. Cut `vX.Y.(Z+1)` from a clean commit on `main`. Do not force-push or delete a published tag — downstream tooling caches resolved versions.

Mark the broken release as pre-release in the GitHub UI if it must be hidden.
