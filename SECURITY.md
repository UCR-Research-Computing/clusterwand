# Security policy

ClusterWand gives AI assistants access to shared research clusters, so we take reports
seriously and answer quickly.

## Reporting a vulnerability

Please **do not open a public issue**. Use GitHub's private reporting:
[Report a vulnerability](https://github.com/UCR-Research-Computing/clusterwand/security/advisories/new).
If that is not possible, email the maintainers through
[UCR Research Computing](https://github.com/UCR-Research-Computing).

Include what you did, what happened, and what you expected. We aim to acknowledge within
two working days and to ship a fix for confirmed issues within a week.

## What is in scope

- Running any command on the cluster that is not one of the allow-listed constructors
  (`internal/backend/command.go`, `files.go`): shell injection, argument smuggling.
- Reading another user's jobs, logs or files without tier R2, or reading hidden or
  credential files.
- Acting (submit, cancel, hold, release) without the two-step confirm, or beyond the caps.
- Calling a tool from a tier the local config does not grant.
- Secrets leaking through tool output or the audit log.
- Prompt injection that makes the server, not the model, do something it should not.

## Design notes for reviewers

- No tool runs arbitrary shell; `TestAllowListIsClosed` pins the command list.
- `scripts/mutation_check.sh` disables each of 148 guards in turn and requires a test
  to fail.
- Text from users and programs is returned in `untrusted` blocks.
- SSH only: ClusterWand runs on the user's own machine (CLI and stdio MCP) and reaches the
  cluster through the user's own OpenSSH config, keys and agent (see
  [docs/SSH.md](docs/SSH.md)). It has no network listener, no sign-in and no credentials of
  its own, and it never sees the user's private keys. It can do on the cluster only what the
  user's login can, and less: only the allow-listed commands.
- Tiers come from the local config file. They protect against a misbehaving model, not
  against the user who owns the config; the cluster's own permissions are the real boundary.
- The audit log and A1 state (plans, tokens, spend ledger) are local files, mode 600.

## Supported versions

Only the latest release is supported.

## Lineage

ClusterWand is forked from ursa-bifrost (v0.10.1). Fixes to shared code are carried across
by hand; report to whichever project you use.
