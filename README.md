<div align="center">

# ClusterWand

**The Bifrost between AI and your HPC cluster.**

Safe, typed MCP tools that let AI assistants do real work on a Slurm cluster over SSH.
One Go binary, both an MCP server and a CLI. No language model inside, and no shell, ever.

[![CI](https://github.com/UCR-Research-Computing/clusterwand/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/UCR-Research-Computing/clusterwand/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/UCR-Research-Computing/clusterwand?sort=semver)](https://github.com/UCR-Research-Computing/clusterwand/releases)
[![Go version](https://img.shields.io/github/go-mod/go-version/UCR-Research-Computing/clusterwand)](go.mod)
[![MCP](https://img.shields.io/badge/MCP-stdio%20%2B%20Streamable%20HTTP-blue)](https://modelcontextprotocol.io)
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
working with any Slurm cluster. v0.1.0 is the renamed fork with identical behaviour; site
profiles and text-format Slurm parsing come next (see [Roadmap](#roadmap)).

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
reuses it: about 0.3 s per command once warm.

```bash
clusterwand config init          # writes ~/.config/clusterwand/config.yaml
```

Edit the file: remove the `ssh.gcloud` block and set your host alias.

```yaml
backend: ssh
ssh:
  host: mycluster                # any ~/.ssh/config alias or user@host
catalog_path: /home/you/clusterwand/catalog.json   # a file in your own home; no admin needed
log_roots: ["/home/{user}/", "/scratch/{user}/"]
tiers: [R1]                      # add A1 for two-step submit/cancel, R2 for staff tools
```

```bash
clusterwand doctor               # checks the config, SSH to the login node and the catalog
clusterwand status
clusterwand jobs --state FAILED
```

**Honest note for v0.1.0:** the tools still expect a cluster catalog JSON on the login node
(`catalog_path`: partitions, prices, modules, recipes; see
[`testdata/catalog.json`](testdata/catalog.json) for the shape), and the defaults are Ursa
Major's. ClusterWand never needs anything installed or changed on the cluster: a normal
researcher login is enough, and the catalog can live in your own home folder. Site
profiles (F1) and `clusterwand discover` (F3) replace the hand-written catalog. On Ursa
Major the defaults work as they are.

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
reasonable to let your client run reads without asking. Remote use (a hosted server with
sign-in, `clusterwand serve`), per-client details and troubleshooting:
[docs/CLIENTS.md](docs/CLIENTS.md).

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
| **R1** read | everyone | `cluster_status`, `partitions`, `jobs_list`, `job_show`, `job_explain`, `job_log_tail`, `job_results`, `modules_search`, `module_show`, `recipes`, `script_check`, `my_usage`, `waste_report`, `storage_usage`, `files_list`, `files_read`, `env_check`, `interactive_help`, `my_allocation`, `results_link`* |
| **A1** act | opt-in per person | `job_submit` + `job_submit_confirm`, `job_cancel` / `job_hold` / `job_release` + `_confirm`, `upload_prepare`*, `uploads_list`* |
| **R2** staff | cluster administrators | `jobs_list_all`, `job_show_any`, `job_explain_any`, `usage_report`, `waste_report_all`, `health`, `ticket_draft` |

\* with a staging bucket configured. Also resources `hpc://catalog`, `hpc://policies` and
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
clusterwand mcp | serve | config init|show|path | doctor | version
```

Submitted jobs run in their own `~/clusterwand-jobs/<stamp>-<name>` folder on the cluster,
so a job's results are exactly that folder.

## Safety model

ClusterWand assumes the model on the other end will sometimes be wrong, and will
occasionally be manipulated by text it reads.

- **No shell, ever.** Remote commands can only be built by constructors in
  `internal/backend/command.go` and `files.go`, each validating its arguments; shell
  templates are fixed text and caller values arrive only as positional parameters.
  `TestAllowListIsClosed` pins the allow-list, and `scripts/mutation_check.sh` disables
  each of 219 guards in turn and requires a test to fail.
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
- **Gentle on the login node.** Short-TTL caches, per-user rate limits, at most 8 SSH
  sessions per connection, no background polling, and no compute on the login node.
- **Other users' jobs** are denied without tier R2.

Details: [SECURITY.md](SECURITY.md) and [docs/SPEC.md](docs/SPEC.md) section 9.

## Architecture

```text
 Claude / Copilot / Gemini / Hermes / Codex / OpenCode / your scripts
        |  MCP over stdio (local)   or   Streamable HTTP + OAuth 2.1 (hosted)
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
| `internal/backend/` | the command allow-list; SSH, IAP and fixture backends |
| `internal/core/` | typed operations, caching, the result envelope, `Call` |
| `internal/rules/` | deterministic diagnosis rules for `job_explain` |
| `internal/policy/` | redaction, untrusted wrapping, tiers, rate limit, audit |
| `internal/mcpserver/` | MCP tools, resources, prompts |
| `internal/server/` | the hosted server (`clusterwand serve`, Google sign-in) |
| `sites/ursa-major/` | Ursa Major only: Cloud Run deploy notes and the ursa-agent dashboard |

## Roadmap

| Phase | What | Status |
|---|---|---|
| F0 | Fork and polish: renamed module, binary and paths; docs; CI | v0.1.0 |
| F1 | Site profiles: every Ursa constant moves to `sites/ursa-major/site.yaml`; charge models (USD per node-hour, SU per core-hour, none); golden tests | planned, v0.2.0 |
| F2 | Slurm adapter: text-format parsers for older Slurm, JSON behind a flag; lab-locked partitions | planned, v0.3.0 |
| F3 | `clusterwand discover`: draft a site profile from a read-only login-node survey | planned, v0.4.0 |
| F4 | Live campaign on a real cluster over plain SSH | planned, v0.5.0 |
| Later | Hosted credential modes (registered keys, OIDC), multi-cluster, SFTP staging | not started |

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
