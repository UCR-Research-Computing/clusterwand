<div align="center">

# ClusterWand

**The Bifrost between AI and your HPC cluster.**

Safe, typed MCP tools that let AI assistants do real work on a Slurm cluster over SSH.
One Go binary on your own machine, both a stdio MCP server and a CLI. No language model
inside, no hosted server, and no shell, ever.

[![CI](https://github.com/UCR-Research-Computing/clusterwand/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/UCR-Research-Computing/clusterwand/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/UCR-Research-Computing/clusterwand?sort=semver)](https://github.com/UCR-Research-Computing/clusterwand/releases)
[![Go version](https://img.shields.io/github/go-mod/go-version/UCR-Research-Computing/clusterwand)](go.mod)
[![MCP](https://img.shields.io/badge/MCP-stdio-blue)](https://modelcontextprotocol.io)
[![Docs](https://img.shields.io/badge/docs-GitHub%20Pages-informational)](https://ucr-research-computing.github.io/clusterwand/)
[![License: MIT](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)

[60-second demo](#60-second-demo) ·
[Install](#install) ·
[Quick start](#quick-start-your-laptop--any-cluster-over-ssh) ·
[MCP clients](#connect-an-ai-assistant) ·
[Safety](#safety-model) ·
[Architecture](#architecture) ·
[Roadmap](#roadmap) ·
[Docs site](https://ucr-research-computing.github.io/clusterwand/)

</div>

---

```text
You:      Why did job 236 fail?
Claude:   [job_explain 236]
          It failed after one minute on the gpu partition: the log says
          "ModuleNotFoundError: No module named 'pandas'". The script calls python
          without an environment. Load the site's PyTorch or SciPy module
          before running it, or create a uv/Pixi environment inside the job.
```

ClusterWand runs allow-listed scheduler queries and fixed diagnosis rules on your
cluster's login node, and hands the assistant JSON with the data, the evidence and the
exact commands it ran. The assistant does the reasoning; ClusterWand does the safe,
checkable part. Nothing a model sends is ever run as a shell command.

## What it is

- **For researchers (tier R1, opt-in A1):** job status and failure diagnosis, log windows,
  batch-script checks before you submit, software module search, usage and waste,
  storage, your own files, interactive-session commands, and (opt-in) a two-step submit,
  cancel, hold and release with caps.
- **For cluster staff (tier R2):** all jobs, any job's diagnosis, usage and waste by user,
  node health, and draft replies to "my job failed" tickets (never sent).
- **For any MCP client:** Claude Code, Claude Desktop, Hermes, Gemini CLI, GitHub Copilot,
  Codex, OpenCode, or your own scripts. Every tool is also a CLI command with `--json`.

ClusterWand is a fork of [ursa-bifrost](https://github.com/UCR-Research-Computing/ursa-bifrost)
v0.10.1, UCR Research Computing's MCP server for its Ursa Major cluster, on its way to
working with any Slurm cluster. v0.1.0 is the renamed fork, cut down to SSH only: it runs
on your machine and reaches the cluster through your own OpenSSH login. The fork's hosted
mode (HTTP server, Google sign-in, IAP backend, Cloud Storage staging) is gone; the local
tools behave as before. Since v0.2.0 everything site-specific lives in a site profile, and
since v0.3.0 Slurm is read as plain text (works on Slurm 21.08 to 25.11; `--json` is an
opt-in per site) and partitions locked to some accounts or groups are only offered to
people who may use them. Since v0.4.0 `clusterwand discover` drafts a profile for a new
cluster from a read-only look at its login node (see below).

## 60-second demo

No cluster needed: the repo ships anonymized recordings of real Slurm output, and the
`fixture` backend replays them.

```bash
git clone https://github.com/UCR-Research-Computing/clusterwand && cd clusterwand
go build -o clusterwand ./cmd/clusterwand          # the Go toolchain is fetched if needed
cat > /tmp/cw-demo.yaml <<EOF
backend: fixture
fixtures_dir: $PWD/testdata
tiers: [R1, R2, A1]
audit_path: /tmp/cw-demo-audit.jsonl
state_path: /tmp/cw-demo-a1.json
EOF
./clusterwand status            --config /tmp/cw-demo.yaml   # nodes, queue, cost per hour now
./clusterwand jobs              --config /tmp/cw-demo.yaml   # your jobs
./clusterwand job explain 236   --config /tmp/cw-demo.yaml   # deterministic diagnosis
./clusterwand modules lammps    --config /tmp/cw-demo.yaml   # software search
./clusterwand mcp               --config /tmp/cw-demo.yaml   # all of it as an MCP server on stdio
```

```text
$ ./clusterwand job explain 236 --config /tmp/cw-demo.yaml
Job 236  FAILED  partition gpul4  exit 1  elapsed 1m00s

[ERROR] Python package not installed in the environment (python-import)
  evidence: log: ModuleNotFoundError: No module named 'pandas'
  fix: Package "pandas" is missing. Load python-sci (CPU) or python-ml (PyTorch/GPU), ...
```

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/UCR-Research-Computing/clusterwand/main/scripts/get.sh | sh
```

This puts the release binary for Linux or macOS (amd64, arm64) in `~/.local/bin`, checks it
against the release's `SHA256SUMS`, and adds the short alias `wand`. Other ways:

```bash
go install github.com/UCR-Research-Computing/clusterwand/cmd/clusterwand@latest
scripts/install.sh            # from a clone: stamps the version, installs clusterwand + wand
```

Release archives on the [releases page](https://github.com/UCR-Research-Computing/clusterwand/releases)
hold `clusterwand`, the `wand` symlink, the license and the client guide. Windows: use WSL.

## Quick start: your laptop + any cluster over SSH

ClusterWand reaches the login node the way you already do: OpenSSH, your own account. If
`ssh mycluster` works without a prompt (a key or an agent; ClusterWand runs ssh with
`BatchMode=yes`), ClusterWand works too. It keeps one multiplexed connection open and
reuses it: about 0.3 s per command once warm. Keys, agents, jump hosts, ControlMaster and
MFA: [docs/SSH.md](docs/SSH.md).

```bash
clusterwand config init          # writes ~/.config/clusterwand/config.yaml
```

Edit the file and set your host alias.

```yaml
site_file: ~/.config/clusterwand/mysite.yaml   # or site: ursa-major (a shipped profile)
backend: ssh
ssh:
  host: mycluster                # any ~/.ssh/config alias or user@host
tiers: [R1]                      # add A1 for two-step submit/cancel, R2 for staff tools
```

```bash
clusterwand doctor               # checks the config, SSH to the login node and the catalog
clusterwand status
clusterwand jobs --state FAILED
```

**Site profiles (v0.2.0).** Everything ClusterWand knows about a cluster beyond plain Slurm
is in a site profile: home and scratch paths, partition roles (probe, GPU, high-memory,
general, fallbacks), CPU notes, the catalog location, the charge model
(`usd_per_node_hour`, `su_per_core_hour` with SU caps, or `none`) and site wording. Ursa
Major's is [`sites/ursa-major/site.yaml`](sites/ursa-major/site.yaml) (`site: ursa-major`);
with no profile a generic one is used (home only, no catalog, no charges, no env_check
partition). A profile of your own needs `approved: true`. Catalog-backed tools (partitions,
modules, recipes, prices) still need a catalog JSON
([`testdata/catalog.json`](testdata/catalog.json) shows the shape), which can live in your
own home folder. ClusterWand never needs anything installed or changed on the cluster.

**Discover a new cluster (v0.4.0).** `clusterwand discover --site mylab --host <ssh alias>`
reads the login node only (Slurm release, `scontrol -o show partition|nodes`, a few
`scontrol show config` keys, your `$HOME` and scratch folders, `module -t avail`) and
writes `sites/mylab/site.yaml` plus a draft `catalog.json` (partitions and modules), both
with `approved: false`. It pins the login node's host keys that your own ssh already
trusts (`host_keys:`): once approved, ClusterWand connects only when the node shows one of
them. Nothing is used until you review the draft and set `approved: true` in both files,
then point `site_file:` at it. `--record DIR` also writes anonymised copies of the probes
as test fixtures; `--compare <profile>` lists where the draft and a reviewed profile differ.

## Connect an AI assistant

Add ClusterWand as a local (stdio) MCP server. The server name `cluster` is your choice;
the tools appear as `cluster_status`, `job_explain`, and so on.

| Client | Setup |
|---|---|
| **Claude Code** | `claude mcp add cluster -- clusterwand mcp` (add `-s user` for every project) |
| **Hermes Agent** | `hermes mcp add cluster --command clusterwand --args mcp` |
| **Gemini CLI** | `gemini mcp add -s user cluster clusterwand mcp` |
| **GitHub Copilot (VS Code)** | `.vscode/mcp.json`: `{"servers": {"cluster": {"type": "stdio", "command": "clusterwand", "args": ["mcp"]}}}`, then enable the tools in Copilot Chat agent mode |
| **Codex CLI** | `codex mcp add cluster -- clusterwand mcp` |
| **OpenCode** | `"cluster": {"type": "local", "command": ["clusterwand", "mcp"]}` under `mcp` in `opencode.jsonc` |
| **Claude Desktop, any JSON config** | `{"mcpServers": {"cluster": {"command": "clusterwand", "args": ["mcp"]}}}` |

Read tools carry `readOnlyHint: true` and acting tools need a second confirm call, so it is
reasonable to let your client run reads without asking. Per-client details and
troubleshooting: [docs/CLIENTS.md](docs/CLIENTS.md).

Things to ask:

- "What's running on the cluster and what is it costing per hour?"
- "Why did my job 236 fail?"
- "Which modules give me LAMMPS with GPU support?"
- "Check this batch script before I submit it."
- "How much did I use last month, and how much was wasted on idle cores?"
- "Write a script for PyTorch on one GPU for 4 hours, show me the plan, then submit it."
- Staff: "Which nodes are down?" / "Draft a reply to this ticket about job 236."

## Tools

| Tier | Who | Tools |
|---|---|---|
| **R1** read | everyone | `cluster_status`, `partitions`, `jobs_list`, `job_show`, `job_explain`, `job_log_tail`, `job_results`, `modules_search`, `module_show`, `recipes`, `script_check`, `my_usage`, `waste_report`, `storage_usage`, `files_list`, `files_read`, `env_check`, `interactive_help`, `my_allocation` |
| **A1** act | opt-in per person | `job_submit` + `job_submit_confirm`, `job_cancel` / `job_hold` / `job_release` + `_confirm` |
| **R2** staff | cluster administrators | `jobs_list_all`, `job_show_any`, `job_explain_any`, `usage_report`, `waste_report_all`, `health`, `ticket_draft` |

Also resources `hpc://catalog`, `hpc://policies` and
prompts `diagnose_job`, `write_batch_script`, `monthly_usage_summary`, `triage_ticket`.
A caller only sees, and can only call, the tiers granted to them. Every result is an
envelope with the data, the evidence, and the scheduler commands that produced it, so an
answer can be checked rather than trusted.

The CLI mirrors the tools (`clusterwand --help` lists every flag):

```text
clusterwand status | partitions | jobs | job show|explain|log <id>
clusterwand modules <query> | module <name> | recipes | check <script.sh>
clusterwand usage | waste | storage | ls | cat | env | interactive | allocation
clusterwand submit <script.sh> | cancel|hold|release <id> | confirm <token>   (tier A1)
clusterwand health | ticket <id>                                              (tier R2)
clusterwand mcp | config init|show|path | doctor | version
```

Submitted jobs run in their own `~/clusterwand-jobs/<stamp>-<name>` folder on the cluster,
so a job's results are exactly that folder; `clusterwand results <id> --download` copies
it to your machine over the same SSH connection.

## Safety model

ClusterWand assumes the model on the other end will sometimes be wrong, and will
occasionally be manipulated by text it reads.

- **No shell, ever.** Remote commands can only be built by constructors in
  `internal/backend/command.go` and `files.go`, each validating its arguments; shell
  templates are fixed text and caller values arrive only as positional parameters.
  `TestAllowListIsClosed` pins the allow-list, and `scripts/mutation_check.sh` disables
  each of 148 guards in turn and requires a test to fail.
- **Read-only by default; acting takes two steps.** State changes exist only in tier A1. A
  prepare call stores the exact action under a random single-use token (10-minute expiry,
  bound to the plan hash); the confirm call accepts nothing but the token. Caps on nodes,
  hours, cost and submits per day are checked at prepare, and the day cap again at confirm.
- **Your files only.** Reads stay inside the caller's own home, scratch and job folders;
  hidden and credential-like files are refused; symlinks are resolved and checked again.
  Log paths come from the job's accounting record, never from the caller.
- **Untrusted text is labelled.** Log lines, scripts and submit lines come back only in
  `untrusted` blocks marked as data, not instructions.
- **Secrets are redacted** (tokens, keys, passwords, JWTs, private keys, credentials in URLs)
  from every output and from the audit log.
- **Everything is audited.** Every call, CLI or MCP, appends a JSON line (mode 600) under
  `~/.local/share/clusterwand/`.
- **Gentle on the login node.** Short-TTL caches, per-user rate limits, one shared SSH
  connection, no background polling, and no compute on the login node.
- **Other users' jobs** are denied without tier R2.

Details: [SECURITY.md](SECURITY.md) and [docs/SPEC.md](docs/SPEC.md) section 9.

## Architecture

```text
 Claude / Copilot / Gemini / Hermes / Codex / OpenCode / your scripts
        |  MCP over stdio, on your machine
        v
 +--------------------------- clusterwand ----------------------------+
 | mcpserver    tools, resources, prompts                             |
 | core.Call    tier check, rate limit, cache, result envelope, audit |
 | rules        deterministic job diagnosis (job_explain)             |
 | policy       redaction, untrusted wrapping, caps                   |
 | backend      allow-listed command constructors, nothing else       |
 +--------------------------------------------------------------------+
        |  one multiplexed OpenSSH connection, as you
        v
 Slurm login node  ->  squeue / sacct / sinfo / scontrol, module, sbatch
```

| Path | What |
|---|---|
| `cmd/clusterwand/` | CLI entry and text renderers |
| `internal/backend/` | the command allow-list; SSH and fixture backends |
| `internal/core/` | typed operations, caching, the result envelope, `Call` |
| `internal/rules/` | deterministic diagnosis rules for `job_explain` |
| `internal/policy/` | redaction, untrusted wrapping, tiers, rate limit, audit |
| `internal/mcpserver/` | MCP tools, resources, prompts |
| `internal/site/`, `sites/` | Site profiles: schema and validation; shipped profiles (`sites/ursa-major/site.yaml`) |

## Roadmap

| Phase | What | Status |
|---|---|---|
| F0 | Fork and polish: renamed module, binary and paths; docs; CI | v0.1.0 |
| F1 | Site profiles: every Ursa constant moves to `sites/ursa-major/site.yaml`; charge models (USD per node-hour, SU per core-hour, none); golden tests | done, v0.2.0 |
| F2 | Slurm adapter: plain-text reads as the primary path (Slurm 21.08 to 25.11), JSON behind a site flag; lab-locked partitions (AllowAccounts/AllowGroups) | done, v0.3.0 |
| F3 | `clusterwand discover`: draft a site profile from a read-only login-node survey; host-key pin; review gate | done, v0.4.0 |
| F4 | Live campaign on a real cluster over plain SSH ([report](docs/LIVE_CAMPAIGN_F4.md)) | done, v0.5.0 |
| Later | Multi-cluster configs | not started |

## Development

```bash
make check                  # gofmt, go vet, go test -race, build   (before every commit)
scripts/mutation_check.sh   # every safety guard must be caught by a test
scripts/mcp_smoke.py        # drives `clusterwand mcp` over stdio like a real client
```

Tests run against anonymized recordings of real cluster output in `testdata/`. Read
[CONTRIBUTING.md](CONTRIBUTING.md) first; the no-shell and two-step rules are not
negotiable. Changes: [CHANGELOG.md](CHANGELOG.md). Design and history:
[docs/SPEC.md](docs/SPEC.md).

## Credits

ClusterWand is forked from [ursa-bifrost](https://github.com/UCR-Research-Computing/ursa-bifrost)
by [UCR Research Computing](https://github.com/UCR-Research-Computing), with its full
history, tests and mutation guards. Released under the [MIT License](LICENSE).
