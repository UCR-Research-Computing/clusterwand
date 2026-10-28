# CLAUDE.md

Guidance for AI coding agents working in this repo.

## What this is

ClusterWand: the Bifrost between AI and your HPC cluster. One Go binary (`clusterwand`,
alias `wand`) that is both a CLI and a local MCP server (stdio). It gives AI assistants typed,
safe tools for a Slurm cluster the person can already reach with their own SSH login.
Forked from ursa-bifrost v0.10.1. The server holds no language model. Spec: `docs/SPEC.md`;
keep its status and change log current when behaviour changes.

SSH only: ClusterWand runs on the person's machine and uses their own OpenSSH config, keys and
agent. There is no OAuth, no hosted/HTTP server, no cloud-provider backend. Clusters are never
changed: everything runs in user space as the person (scheduler queries, sbatch, file reads in
their own home and scratch).

## Layout

- `cmd/clusterwand/` CLI entry (`main.go`) and text renderers (`print.go`).
- `internal/backend/` the command allow-list (`command.go`), SSH backend (`ssh.go`),
  fixture backend for tests (`fixture.go`).
- `internal/slurm/` Slurm output parsers.
- `internal/core/` typed operations; `service.go` has caching, the result envelope and
  `Call` (tier check, rate limit, audit) that every CLI command and MCP tool goes through.
- `internal/rules/` deterministic diagnosis rules for `job_explain`.
- `internal/policy/` redaction, untrusted wrapping, tiers, rate limit, audit log.
- `internal/mcpserver/` MCP tools/resources/prompts over core.
- `sites/<name>/` per-cluster site profiles (Ursa Major is `sites/ursa-major/`).

## Rules

- Gauntlet before every commit: `make check` (gofmt, vet, `go test -race`, build).
- Never add a tool or command that runs arbitrary shell. New remote commands are new
  constructors in `backend/command.go` with validated arguments, and must be added to
  `TestAllowListIsClosed`. State-changing commands are marked `write` and live only behind
  tier A1's two-step prepare/confirm flow (`internal/core/a1.go`); every new guard gets a
  line in `scripts/mutation_check.sh`.
- Every new tool goes through `core.Call` (tier-checked and audited) and returns
  user/program-written text only via `policy.Wrap` (untrusted block).
- No site-specific names (partitions, paths, hostnames, hardware) in code; they belong in the
  site profile.
- Every diagnosis rule needs a sample in `TestEachLogRuleFires`; prefer real failed-job
  logs as fixtures (anonymize with `scripts/make_fixtures.py`).
- Fixtures must not contain real usernames, uids or internal IPs; CI greps for it.
- Go toolchain: the Makefile pins `GOTOOLCHAIN`.
- Bump `internal/version` and tag releases (`vX.Y.Z`); `scripts/install.sh` stamps the version
  from `git describe`.
- Live checks against a cluster are read-only (`clusterwand doctor`, `status`, `jobs`,
  `job explain`) except jobs submitted through the two-step flow. Never run compute on the
  login node. Never change the cluster.
