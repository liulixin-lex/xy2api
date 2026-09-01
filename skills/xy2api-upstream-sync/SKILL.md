---
name: xy2api-upstream-sync
description: Synchronize a stable Sub2API release into XY2API using pinned three-way merges, compatibility audits, review PRs, RC validation, and formal release gates. Use for upstream analysis, sync preparation, conflict resolution, merge, release, rollback validation, or troubleshooting this workflow; do not use for ordinary feature work or dependency-only updates.
---

# XY2API Upstream Sync

Use the repository's synchronization machinery instead of rebuilding the process from memory:

- `tools/upstream-sync/sync.py`: prepare, report, normalize, and audit.
- `tools/upstream-sync/policy.json`: ownership, compatibility invariants, and named patch tests.
- `UPSTREAM_BASE.json`: immutable provenance and current compatibility baseline.
- `docs/UPSTREAM_SYNC.md`: repository policy and project-specific contracts.
- `.github/workflows/upstream-sync.yml` and `release.yml`: draft PR and release automation.

Before starting a new sync, run `python skills/xy2api-upstream-sync/scripts/doctor.py --strict`. On a completed sync branch, run the same command with `--audit` but without `--strict`, because prepare-readiness intentionally requires `main`.

## Select The Requested Stage

Determine the furthest stage the user requested before mutating anything:

1. **Analyze**: inspect the upstream release and produce a three-way impact report only.
2. **Prepare**: create the sync branch, controlled merge, provenance, and draft PR.
3. **Integrate**: resolve manual decisions, pass gates, review, and merge to `main`.
4. **Release**: create and validate RC, then promote through a separate formal release PR.
5. **Full flow**: complete all stages, including post-release verification and cleanup.

Pushes, PR mutations, merges, tags, releases, branch protection changes, and production-like smoke tests require user intent covering that stage. A request to complete the full synchronization and release flow covers those expected mutations, but never authorizes moving an existing tag, rewriting history, changing production data, or force-pushing.

Read [references/runbook.md](references/runbook.md) when executing any stage beyond analysis. Read [references/environment-adapters.md](references/environment-adapters.md) only when selecting tools or handling an environment-specific failure.

## Non-Negotiable Invariants

- Sync only a stable, non-draft, non-prerelease **annotated tag**. Pin its tag object and target commit under `refs/tags/sub2api/<tag>`; never sync moving `upstream/main`.
- Use the real merge base and `git merge --no-ff`; do not rebase, squash, overwrite the tree, or reconstruct the release with cherry-picks.
- Keep upstream merge, manual resolutions, XY2API compatibility patches, generated output, provenance, and version promotion in separate commits when those changes exist.
- Apply module-path normalization only to upstream-changed `.go` and `.proto` files. Never run a repository-wide `sub2api -> xy2api` replacement.
- Treat published migrations, plugin v1, HTTP routes, Redis/browser keys, stable UUID/KDF inputs, WebSocket subprotocols, legacy paths/binaries, and third-party compatibility markers as contracts.
- Keep `VERSION` as the XY2API product version and `SUB2API_COMPAT_VERSION` as the audited Sub2API baseline.
- Scheduled automation may create one draft sync PR, but it must never merge or release automatically.
- `main` must end with zero unresolved conflicts, zero pending provenance entries, zero unclassified fork differences, and all required checks passing.
- Do not reuse, rename, delete, or stop existing deployment containers, volumes, databases, Redis data, or secrets during validation. Use isolated names and restore any local runtime state you changed.

## Efficient Execution Rules

- Freeze exact identifiers first: tag, tag object, target commit, merge base, fork start, branch, RC version, and final version.
- Use the generated three-way report and ownership policy to focus review on overlap and affected domains; do not reread the entire repository.
- Run cheap deterministic gates before long suites: clean tree, tool tests, sync audit, `git diff --check`, conflict-marker scan, migration manifest, and Compose parsing.
- Run impacted local tests first. Let CI provide the complete fixed-toolchain matrix when the local machine lacks Go, Node, Docker, or platform-specific runners.
- Monitor checks by required context. Push and PR triggers may produce duplicate runs; do not restart successful work solely because two instances exist.
- After every external mutation, re-read the authoritative state before continuing: PR head SHA, merge commit, tag target, workflow result, release flags, asset digests, and image manifest.
- Retry transient network operations only a small bounded number of times. Never hide a persistent connectivity failure behind an unbounded loop.

## Stop Conditions

Stop before merge or release when any of these occurs:

- The upstream tag object or target commit differs from the frozen value.
- The tag is lightweight, malformed, draft, prerelease, or has a failing signature verification.
- A conflict is outside `manual_merge`, a published migration changed, or a fork difference is unclassified.
- A manual resolution remains `pending`, a named patch lacks its bound test, or generated output is dirty after regeneration.
- Required CI/security checks fail, the PR head moved after review, or the release tag already exists with a different target.
- Upgrade preflight reports ambiguous data, volume, Redis, path, or key ownership.

Do not turn a blocked state into a partial release. Record the exact blocker and leave `main` and existing tags unchanged.

## Completion Evidence

Report only the high-signal evidence:

- upstream tag, tag object, target commit, and merge base;
- sync PR, release PR, and their merge commits;
- RC and formal tag objects and target commits;
- required check and release workflow results;
- migration count/checksum result, asset checksum result, image platforms/digest, and smoke/rollback result;
- cleanup status, remaining known follow-ups, and anything skipped with its reason.

Do not claim completion until the formal release is non-draft/non-prerelease, the published artifacts are independently verified, `main` is clean and audited, and temporary validation resources are removed.
