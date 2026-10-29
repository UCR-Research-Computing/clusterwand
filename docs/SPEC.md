# ClusterWand specification

| | |
|---|---|
| Status | v0.6.0 (2026-10-29). CLI and MCP server over stdio on the person's own machine, SSH only. Site profiles, plain-text Slurm reads, `discover`, live-tested on Ursa Major ([LIVE_CAMPAIGN_F4.md](LIVE_CAMPAIGN_F4.md)). |
| Owner | Chuck Forsyth (UCR Research Computing) |
| Repo | https://github.com/UCR-Research-Computing/clusterwand (MIT) |
| Binary | `clusterwand`, alias `wand` |
| Lineage | Forked 2026-10-28 from ursa-bifrost v0.10.1 (77cb4cd). The inherited spec, with its hosted server, IAP, Cloud Storage staging, dashboard, program clients and release history, is kept unchanged in [history/URSA_BIFROST_SPEC_v0.10.1.md](history/URSA_BIFROST_SPEC_v0.10.1.md). |

Keep the status row and the change log at the end current when behaviour changes.

## 1. What it is

ClusterWand is a small, deterministic Go program that gives AI assistants typed, safe tools
for a Slurm cluster the person can already reach with their own SSH login. An assistant
(Claude Code, Hermes, Gemini CLI, Copilot, a script) asks typed questions such as "why did
job 260 fail", "what is idle right now" or "which modules provide GROMACS with GPU
support", and gets JSON back with the data, the evidence and the exact scheduler commands
that produced it.

The same binary is a CLI (`--json` on every command) and a local MCP server
(`clusterwand mcp`, stdio). It holds no language model: it runs allow-listed scheduler
queries and fixed diagnosis rules, and the calling assistant does the reasoning. That keeps
it cheap, predictable and testable.

Clusters are never changed. Everything runs in user space, as the person: scheduler
queries, `sbatch` through a two-step confirm, and reads in their own home and scratch.
Nothing is installed on the cluster.

## 2. Users and tiers

| Tier | Who | Can |
|---|---|---|
| R1 | Everyone (default) | Own jobs, public cluster facts, modules, own files, usage, waste |
| R2 | Cluster staff | Every user's jobs and usage, node health, ticket drafts (never sent) |
| A1 | Opt-in per person | Submit, cancel, hold and release own jobs, two-step with caps |

Tiers come from the local config (`tiers:`). A tool that needs a tier the config does not
grant is not registered with the MCP client and is refused on the CLI.

Typical uses: a researcher's assistant writes a batch script that fits the cluster's real
partitions and modules, checks it, submits it with approval and explains a failure; staff
triage "my job failed" tickets; anyone looks up software or checks where their quota went.

## 3. Goals and non-goals

Goals
- G1 Typed, documented tools with JSON in and out; no free-form shell access.
- G2 Read-only by default. Anything that spends allocation or changes state needs tier A1
  and a confirm step.
- G3 Runs as the person, through their own SSH; every call is audited locally.
- G4 Deterministic: same inputs, same outputs; no model calls inside.
- G5 Any Slurm cluster: site facts live in a profile, not in code; Slurm 21.08 to 25.11.
- G6 Gentle on the login node: caching, rate limits, one shared connection, no background
  polling, no compute on the login node.

Non-goals
- N1 No arbitrary command execution tool ("run this shell command"). Ever (section 9.5).
- N2 No Slurm administration (node states, reservations, accounts, QOS).
- N3 No compute on the login node. env_check runs a one-core job on the site's probe partition.
- N4 No hosted or HTTP mode, no OAuth, no cloud-provider backend, no file staging service.
- N5 No changes to the cluster: no installs, no files outside the person's own folders.

## 4. Architecture

```
  MCP clients                        clusterwand (your machine)                  Cluster
  ---------------------     ------------------------------------------      ----------------
  Claude Code, Hermes, stdio| mcpserver: tools, resources, prompts    |
  Gemini CLI, Copilot ----->| CLI: same operations, text or --json    |
  scripts (CLI --json)      | core.Call: tier, rate limit, audit      |      your OpenSSH
                            | core: typed operations, caches, rules   | ---> (ControlMaster)
                            | policy: redaction, untrusted, tiers     |      login node:
                            | backend: allow-listed argv over ssh     |      squeue, sacct,
                            | site profile: paths, roles, charge      |      sinfo, scontrol,
                            -------------------------------------------      sbatch, module
```

| Package | Role |
|---|---|
| `cmd/clusterwand` | CLI entry and text renderers; `discover` |
| `internal/backend` | the command allow-list (`command.go`, `files.go`), the SSH backend, the fixture backend for tests |
| `internal/slurm` | parsers for Slurm text and JSON output |
| `internal/core` | typed operations; `service.go` holds caching, the result envelope and `Call` |
| `internal/rules` | deterministic diagnosis rules for `job_explain` |
| `internal/policy` | redaction, untrusted wrapping, tiers, rate limit, audit log |
| `internal/mcpserver` | MCP tools, resources and prompts over core |
| `internal/site` | site profiles (`sites/<name>/site.yaml` embedded, or a local file) |
| `internal/discover` | read-only survey that drafts a site profile |

Every CLI command and MCP tool goes through `core.Call` (tier check, rate limit, audit
record), and every remote command is a `backend.Command` built by a validating constructor.
Results share one envelope: `data`, `source` (backend and the redacted commands run),
`as_of`, `truncated`, and `untrusted_note` when the answer holds user- or program-written
text.

Caches are per process with short TTLs from the config (defaults: queue 20 s, nodes 30 s,
accounting 60 s, catalog and modules 1 h; storage 5 min). Writes are never cached.

## 5. Identity: SSH only

ClusterWand reaches the cluster only through the person's own OpenSSH: their
`~/.ssh/config`, keys, agent, `ProxyJump` or `ProxyCommand`, and MFA. Config names one host
or alias (`ssh.host`); ClusterWand adds `BatchMode=yes`, a `ControlMaster=auto` socket with
`ControlPersist` (default 900 s), keep-alives and a connect timeout, so MFA is answered once
per persist window and every command shares one connection. An ssh exit 255 drops the
connection setup so the next call reconnects. Setup and troubleshooting: [SSH.md](SSH.md).

When an approved site profile lists `host_keys` (section 8), ssh accepts only those keys:
they are written to `<state dir>/known_hosts/<site>` and ssh runs with
`StrictHostKeyChecking=yes`, that file as the only known_hosts file, `HostKeyAlias`,
`UpdateHostKeys=no` and its own control socket prefix. A changed key fails to connect;
updating it is a person's edit of the profile.

The cluster user is `id -un` on the login node (or `cluster_user` in config). Every tool is
scoped to that user unless the config grants R2. The audit log names the local OS user as
the caller and `cli` or the MCP client's name as the client.

## 6. Site profiles

A site profile holds everything about one cluster that is not plain Slurm. Shipped profiles
are embedded from `sites/<name>/site.yaml` (today: `ursa-major`); a person's own is
`site_file:`. Selection: `site_file`, else `site`, else a `cluster` name that matches a
shipped profile, else the generic profile. An unknown profile, or one with
`approved: false`, is an error, never silently generic.

- Fields: `name`, `title`, `description`, `approved`, `help` (who to ask), `catalog`
  (absolute path of a catalog JSON on the login node) or `catalog_file` (a local catalog
  next to a `site_file` profile), `connect` (how people reach the login node),
  `paths.home` / `paths.scratch` (absolute, one `{user}`), `paths.log_roots`, `cloud_nodes`
  (nodes boot on demand and bill while up), `stockout_reasons`, `partitions` (`probe` for
  env_check, `gpu`, `highmem`, `general`, `fast`, `fallback_idle`, `fallback_cold`,
  `avx2_only`, per-partition `cpu` notes, `cpu_default`), `modules` (`python`, `gpu`),
  `charge`, `allocation.covered_by`, `slurm.json`, `host_keys`, `discovered`, and `text`
  (site wording by key, with `{help}`, `{title}`, `{probe}`, `{gpu}` and similar
  placeholders). Unknown fields are refused; every partition name is checked against the
  Slurm name pattern.
- Charge models:
  - `usd_per_node_hour`: prices from config `usd_per_node_hour`, then `charge.rates`, then
    the catalog; caps in dollars.
  - `su_per_core_hour`: `charge.rates` per partition times cores per node; job and day caps
    in SU from config `caps.max_su_per_job` / `max_su_per_day` or the profile's
    `charge.max_su_per_job` / `max_su_per_day`. A submit with no SU caps, or to an unpriced
    partition, is refused. Figures appear as `est_charge` / `charge_per_node_hour` with
    `charge_unit`, never in `*_usd` fields.
  - `none`: no figures and no cost caps; node, hour and submit-count caps still hold.
- Config values (`catalog_path`, `log_roots`, `env_check_partition`, `usd_per_node_hour`)
  win over the profile. With no probe partition env_check refuses; with no catalog the
  catalog tools say so.
- Site wording replaces generic text by key, tool descriptions included. Keys that depend
  on cost visibility come in pairs: `tool_cluster_status` / `tool_cluster_status_nocost`
  and `tool_partitions` / `tool_partitions_nocost` (section 9.6).

Partition limits (MaxTime and similar) come from Slurm and the catalog at run time, not
from the profile.

## 7. Slurm adapter

### 7.1 Reads

ClusterWand reads Slurm as plain text by default; the formats read the same on Slurm 21.08
to 25.11. `--json`, which needs the data_parser of the Slurm release that prints it (25.11:
v0.0.44), is used only when the site profile sets `slurm.json: true`. All reads go through
one place in core (`internal/core/slurmread.go`) and parsers in `internal/slurm`.

| Read | Command (fixed template or argv; selections validated and passed as positional parameters) |
|---|---|
| Queue | `squeue -h -O <fields>` (fields split by `\x1f`, epoch times) |
| Per-job accounting | `sacct -P --noconvert --delimiter=\x1f -o <fields>` with a header line, columns mapped by name, JobName last |
| List accounting | `sacct -n -P --noconvert -o <17 fields>` summary (below) |
| Nodes | `scontrol --version; scontrol -o show nodes` |
| Partitions and sharing | `scontrol -o show partition`; `sinfo -h -o %R\|%h` |
| Stored script | `sacct -n -B -j <id>` |
| Access | `sacctmgr -n -P show user <me> withassoc format=DefaultAccount,Account,Partition`; `id -Gn` |
| Version | `sinfo --version` (doctor) |

List-type accounting (`jobs_list`, `my_usage` / `usage_report`, `waste_report(_all)`,
`health`'s failure rate, `my_allocation`) uses a plain-text summary: JobIDRaw, User,
Partition, State, Reason, ExitCode, NodeList, NNodes, Submit, Start, End, ElapsedRaw,
TotalCPU, AllocTRES, MaxRSS, Restarts, and JobName last so a `|` in a name stays part of
it. Job rows carry the totals; step rows contribute only their MaxRSS. Measured on Ursa
Major for one user and 7 days: 0.2 s, against 19.6 s for `sacct --json` (nearly all of it
JSON serialization), with the same numbers field by field.

Slurm 21.08 has no `Restarts` or `StdOut`/`StdErr` fields: the templates ask without them
and leave the column empty. A finished job's log path is then unknown and `job_log_tail`
says so; it is never guessed. Where squeue and sacct print an `--output` pattern,
ClusterWand expands it as Slurm does (`%j %A %a %u %x %N %%`, zero padding, relative to the
working directory). Slurm encodes "no time" as 4294967294 in some fields; timestamps from
then on are treated as unset.

### 7.2 Shared partitions

Whether a partition shares nodes between jobs is read live (`sinfo -h -o %R|%h`): any
OverSubscribe value other than `EXCLUSIVE` counts as shared. If the read fails, every
partition counts as exclusive: whole-node pricing is the higher estimate, and the core rule
below never fires on a guess. `cluster_status` notes name the shared and whole-node
partitions.

| Where | Behaviour on a shared partition |
|---|---|
| `script_check` | Error when the script asks for no cores (none of `--cpus-per-task`/`-c`, `--ntasks`/`-n`, `--ntasks-per-node`, `--exclusive`). Warning when it counts every core on the node (`os.cpu_count()`, `nproc --all`, `/proc/cpuinfo`) while holding fewer. The answer carries `cores` |
| `job_submit` | The error blocks the plan; checked again against an override partition |
| Worst case, caps, estimates, `interactive_help` | price x nodes x share x hours; share = max(cores / node cores, memory / node memory), capped at 1; `--exclusive` is 1 |
| Spent cost (`jobs_list`, `job_show`, `my_usage`, `usage_report`, `waste_report`, `my_allocation`) | the share of the node the job was allocated (accounting TRES or squeue); 1 on exclusive partitions |

### 7.3 Lab-locked partitions

A partition may be limited by `AllowAccounts`, `DenyAccounts` or `AllowGroups`; Slurm
rejects anyone else's job. ClusterWand reads the limits live and, only when some partition
is limited, the caller's associations and groups (`internal/core/access.go`):

- `partitions` lists only partitions the caller may use and marks limited ones with
  `restricted_to`.
- `script_check` refuses a partition (or a member of a partition list) that the caller's
  default account or the script's `--account` may not use, or that their groups exclude,
  and names an account of theirs that would work. A `--account` the caller has no
  association with is refused. `job_submit` blocks on these errors.
- `interactive_help` refuses such a partition (or adds `--account=`); stockout fallbacks
  never suggest one.
- An association scoped to one partition counts only there. When `scontrol`, `sacctmgr` or
  `id` cannot be read, nothing is hidden or refused on a guess (Slurm still enforces its
  rule at submit time).

## 8. Discover

`clusterwand discover --site <name> --host <ssh alias> [--out sites] [--record DIR]
[--from DIR] [--compare <profile> [--compare-catalog FILE]] [--force]` drafts a site profile
for a cluster the person can reach with their own SSH (`internal/discover`,
`cmd/clusterwand/discover.go`).

- Probes (read-only, login node only, one audited `core.Call` "discover", tier R1, generic
  profile): `id -un`, `sinfo --version`, `scontrol -o show partition`,
  `scontrol --version; scontrol -o show nodes`, `scontrol show config` (15 keys kept in
  `discover.ConfigKeys`, the rest dropped before use or recording), a fixed `bash -lc`
  template that prints `$HOME`, `$SCRATCH`, which of a fixed list of folders named after
  `$USER` exist (`test -d` only) and the module system, and `module -t avail`.
- Output: `sites/<name>/site.yaml` (`approved: false`, `catalog_file: catalog.json`,
  `charge: none`, `host_keys`, `discovered:` provenance, review notes as comments) and
  `catalog.json` (`clusterwand-catalog/1`, `"approved": false`). Never overwrites an
  existing file; `--force` replaces only an unapproved draft.
- Inference (facts only; a role it cannot infer stays empty with a note): general = the
  Default=YES partition; probe = the shortest open partition with MaxTime <= 1 h, else an
  open one named check/debug/test/short/devel; gpu = the open partition with GPUs and the
  most nodes; highmem = the open partition with the most memory per node, at least 1.5x
  general; avx2_only = every node advertises avx2 and no avx512*; cloud_nodes = any node in
  state CLOUD; python modules by name. "Open" = no account or group limit.
- Review gate: a profile with `approved: false` is refused, and so is a local catalog that
  says `"approved": false`, until a person edits them.
- Host keys: after the probes succeed (ssh's own host-key check passed), discover records
  the keys the person's ssh already trusts for that alias (`ssh -G`, `ssh-keygen -F`;
  revoked, CA and malformed lines skipped).
- `--record DIR` writes anonymised probe files (user name, private IPv4 addresses, accounts
  and groups in access lists, people in node reasons replaced); `--from DIR` replays one.

On Ursa Major the draft matches the hand-written profile on every inferred field
(`TestBuildMatchesHandProfile`). Only a person adds the charge model and prices, the
published catalog, `fast`, stockout fallbacks, CPU notes and AVX2-only partitions (Ursa's
nodes advertise no CPU features), stockout reasons, the help desk and wording keys.

## 9. Tool catalog

Every tool returns the envelope from section 4. Text written by users or programs (log
lines, job names, scripts, file contents, submit lines) is returned only inside
`untrusted` blocks. As built, with all tiers granted: 19 R1, 7 R2 and 8 A1 tools, 34 in
all; 2 resources; 4 prompts.

### 9.1 R1 (own jobs and cluster facts)

| Tool | Inputs | Returns |
|---|---|---|
| `cluster_status` | none | Per partition: nodes by state (powered up, allocated, idle, booting, down), running and pending jobs, pending reasons, sharing notes; the current burn per hour when the caller sees dollars |
| `partitions` | none | Partitions the caller may use: nodes, cores and memory per node, GPUs, time limit, CPU note, spot, default, `restricted_to`; price per node-hour when the caller sees charges |
| `jobs_list` | `state?`, `since?`, `limit?`, `job_ids[]?` | Caller's jobs (queue plus recent accounting, newest first), 50 rows unless `limit` (cap `limits.list_rows`); `job_ids` (up to 100) reads just those jobs, uncached |
| `job_show` | `job_id`, `include_script?` | Request vs use (CPU efficiency, peak memory), exit code, signal, nodes, timings, steps, log paths; a pending reason decoded with advice; `script_note` when the cluster stored no script |
| `job_explain` | `job_id`, `lines?` | Deterministic findings (section 10) with evidence and suggestion, plus the redacted log tail as untrusted data |
| `job_log_tail` | `job_id`, `stream`, `lines`, `start_line?`, `grep?` | A window of up to 1000 lines from the end or from `start_line`, or matching lines with context; `total_lines`, `first_line`, `last_line` for paging. The path comes from the job record, never from input |
| `job_results` | `job_id`, `prefix?`, `pattern?`, `offset?`, `limit?`, `read?`, `read_offset?`, `read_bytes?`, `grep?`, `download?`, `files?` | The job's working directory, paged; one text file read in chunks or searched; download to `results_dir/<job_id>` over the same SSH connection |
| `storage_usage` | none | Space used in home and scratch (largest top-level entries first, `du` 8 at a time within a 40 s budget) and filesystem fullness; points at shared reference data when the catalog lists it |
| `files_list` | `path?`, `pattern?`, `offset?`, `limit?` | One folder under the caller's home or scratch |
| `files_read` | `path`, `offset?`, `bytes?`, `grep?` | One text file under home or scratch, in chunks or searched |
| `env_check` | `modules[]?`, `commands[]?` | Loads modules in a one-core job on the probe partition and reports which load, the module list, and where each command resolves with its version |
| `interactive_help` | `partition?`, `nodes?`, `cpus?`, `gpus?`, `time?`, `memory?` | The exact `srun --pty`/`salloc` command and how to reach the login node, with hourly and worst-case cost when shown. Runs nothing |
| `modules_search` | `query?` | Matching modules and versions with MPI prerequisites and GPU builds, prebuilt containers, recipes and shared reference databases from the catalog |
| `module_show` | `name`, `mpi?` | What the module sets; MPI-built packages are shown after loading their MPI |
| `recipes` | `query?` | Known-good recipes and site rules from the catalog |
| `script_check` | `script` | Static check: partition exists and is allowed, limits fit (MaxTime too), modules exist with their MPI prerequisite, GPUs match, core request on shared partitions, AVX-512 builds on AVX2-only nodes, arrays and database downloads, worst-case cost. Never submits |
| `my_usage` | `since?`, `until?`, `group_by?` | Node-hours, core-hours, GPU-hours, CPU efficiency and estimated cost, by partition (default), state, user or day |
| `waste_report` | `since?`, `cpu_threshold?`, `min_node_hours?` | Avoidable use in the caller's jobs: low CPU efficiency, idle time before a timeout, keep-warm jobs, repeated fast failures, oversized memory, idle powered-up nodes; sorted by wasted node-hours with suggestions |
| `my_allocation` | none | Current-period allocation and use for the caller and their lab (section 9.6) |

### 9.2 R2 (staff)

Registered only when the config grants R2. Descriptions start with `[staff]`.

| Tool | Inputs | Returns |
|---|---|---|
| `jobs_list_all` | `user?`, `state?`, `since?`, `limit?` | As `jobs_list`, every user or one |
| `job_show_any`, `job_explain_any` | `job_id` (+ `include_script?` / `lines?`) | As R1, for any user's job |
| `usage_report` | `since?`, `until?`, `group_by?`, `user?` | Usage of every user (or one), by user, partition or state |
| `waste_report_all` | `since?`, `cpu_threshold?`, `min_node_hours?`, `user?` | `waste_report` over every user's jobs |
| `health` | none | Down/drained nodes with reasons, slow boots (stockouts), long-pending and launch-failed jobs, recent node failures, high failure rate, idle billing nodes; `ok` false on any error |
| `ticket_draft` | `job_id`, `ticket_text?` | What happened, evidence, suggested fix, a reply draft, confidence. A COMPLETED exit-0 job is reported as a success. The researcher's text is untrusted and never copied into the reply. Never sent anywhere |

### 9.3 A1 (own jobs, two steps)

Registered only when the config grants A1. Descriptions start with `[act]`; annotations say
not read-only (`job_cancel_confirm` is also destructive).

| Tool | Inputs | Behaviour |
|---|---|---|
| `job_submit` | `script`, `partition?`, `nodes?`, `time?`, `job_name?` | Runs `script_check` (errors block), enforces caps, runs `sbatch --test-only` with the enforced flags, and returns the plan, estimated start, worst-case cost, allocation impact and a single-use `confirm_token`. Nothing is submitted |
| `job_submit_confirm` | `confirm_token` | Re-checks the plan hash, the day cap and the submit count, writes the script to a new `~/clusterwand-jobs/<stamp>-<name>/` and runs `sbatch --parsable --chdir --partition --nodes --time --job-name --comment=clusterwand:<plan hash>` |
| `job_cancel`, `job_hold`, `job_release` | `job_id` | Own jobs only; state checked (only pending or running jobs can be cancelled, only pending ones held or released); returns a plan and token |
| `job_cancel_confirm`, `job_hold_confirm`, `job_release_confirm` | `confirm_token` | Ownership and state checked again, then `scancel` / `scontrol hold\|release`. Cancelling a never-started job releases its worst case from the day cap |

### 9.4 Resources and prompts

- Resources: `hpc://catalog` (the cluster catalog, prices stripped when the caller does not
  see cost) and `hpc://policies` (tiers, approval, untrusted data, caps, cost model).
- Prompts: `diagnose_job`, `write_batch_script`, `monthly_usage_summary`, `triage_ticket`.
  They are templates for the client; ClusterWand runs no model.

### 9.5 Never exposed

A shell or command passthrough; file writes on the cluster other than a submitted job's own
folder; `scontrol update`, `sacctmgr` changes, reservations or QOS; cancelling other users'
jobs; reading outside the caller's own home, scratch and job folders; reading hidden or
credential-like files anywhere. A shell would bypass every guarantee in section 11 (the
allow-list, argument validation, caps, the audit of exact commands, the untrusted-text
rule). People who need one already have it under their own login; `interactive_help`
writes the command.

### 9.6 Cost visibility and allocations

- `show_cost: false` hides every cost figure. `show_cost_tiers: [...]` shows them only to
  callers holding one of those tiers (unset: everyone). A site with charge model `none` has
  no figures. Hidden figures are removed from every answer: cluster burn and partition
  prices, job and usage costs, waste totals and suggestion text, `interactive_help`,
  `script_check` estimates and `hpc://catalog` prices.
- Tool descriptions follow the same rule: `cluster_status` mentions $/hour only when the
  caller sees dollars, and `partitions` mentions a price only when the caller sees charges.
- Plans (`job_submit`, `job_cancel`) always keep the worst case and the caps, so a person
  approving a job sees what the safety caps measure.
- Allocations are optional local config (`allocation: {person_usd, lab: {id, name,
  allocation_usd, members}}`). `my_allocation` returns the period (the calendar month in
  UTC; it resets at 00:00 UTC on the 1st), `basis` "list-price estimate", `covered_by` from
  the site profile, `person` and `lab` (`null` when not configured) with allocation, used,
  left (never negative), percent used and status (`ok` under 80%, `warn` from 80%, `over`
  from 100%), and `enforcement: "warn"`. Dollars round to cents, percentages to 0.1%.
- A job counts in the month of its end time, else its start, else its submit time, so a job
  that crosses a month boundary counts wholly in the month it ends; a running job counts
  the time used so far. Lab use is one accounting query over `lab.members` (cluster user
  names; the caller is always included). A wrong member list undercounts silently.
- R1 callers see only the lab total and member count. R2 callers also get one note per
  member (`"<user>: $<used>"`, free text in `notes`).
- `job_submit` plans add `allocation: {person_pct_after, lab_pct_after, warning}` from the
  worst case, with a warning from 80% and from 100%. Allocations never block a submit.

## 10. Diagnosis rules

Fixed rules over the accounting record and the redacted log tail (`internal/rules`). Each
finding carries the evidence that fired it, a severity and a suggestion (site wording can
replace a suggestion by key). Every log rule has a sample in `TestEachLogRuleFires`, mostly
from real failed jobs.

State and accounting rules:

| Rule | Fires on | Severity |
|---|---|---|
| `oom` | state OUT_OF_MEMORY | error |
| `timeout` | state TIMEOUT, or `DUE TO TIME LIMIT` in the log | error |
| `node-fail` | state NODE_FAIL (with the restart count) | error |
| `preempted` | state PREEMPTED | warning |
| `cancelled` | state CANCELLED (who and when, if known) | info |
| `pending` | a pending job: reason decoded with advice | info |
| `memory-near-limit` | peak memory at 95% or more of the allocation (not when already OUT_OF_MEMORY) | warning; error when the job FAILED |
| `exit-signal` | FAILED with exit code 128+N (signal named), unless a crash or memory rule explains it | error |
| `low-cpu-efficiency` | CPU efficiency below 20% on 4+ cores for 10+ minutes | warning |
| `command-not-found` | exit code 127 without a log | error |
| `script-error` | FAILED and no other rule matched | error |

Log rules (regular expressions over the redacted log tail): `oom-log`, `install-ladder`,
`container`, `illegal-instruction`, `tool-crash`, `glibc`, `module-missing`,
`python-import`, `tls`, `download`, `bad-arguments`, `api-change`, `syntax`, `numerical`,
`missing-feature`, `cuda`, `disk-full`, `permission`, `command-not-found`, `python-error`,
`build-failure`.

Not built: "GPU idle" (GPUs are not in the accounting TRES on the clusters seen so far) and
"login-node misuse" (would need process data from the login node).

## 11. Safety model

### 11.1 Allow-list

Remote commands exist only as constructors in `internal/backend/command.go` and `files.go`.
Each validates its arguments (job ids `^\d{1,10}(_\d{1,7})?$`, partitions, users, paths,
times, module and command names) and builds an argument vector; shell templates are fixed
text and caller values arrive only as positional parameters. `TestAllowListIsClosed` pins
the set of remote programs. State-changing commands are marked `write` and are reachable
only through the A1 prepare/confirm flow (`internal/core/a1.go`).

### 11.2 Approval and caps (A1)

Two layers, both required:
1. Client side: the assistant's own tool-approval prompt.
2. ClusterWand: a single-use `confirm_token` bound to the exact plan hash (script, folder,
   enforced options), expiring after `confirm_ttl_minutes` (default 10). The confirm call
   accepts nothing but the token.

Caps (config `caps`; defaults 4 nodes, 24 h, $25 per job, $50 per day, 20 submits per day;
SU caps on SU sites) are checked at prepare, and the day cap and submit count again at
confirm. The worst case is price x nodes x share x hours x array tasks; an array whose task
count cannot be read, and heterogeneous jobs (`hetjob`/`packjob`), are refused. Spend is
kept in a local ledger (`state_path`, mode 0600, about 40 days kept) under an exclusive
`flock`, so two processes cannot confirm one token.

### 11.3 Untrusted content

Job names, comments, scripts, file contents, submit lines and log lines can carry text
aimed at the assistant. Such text is returned only inside `untrusted` blocks,
length-capped (`limits.untrusted_chars`) and marked as data. No tool acts on content it
read; actions come only from explicit A1 calls with approval. The MCP server instructions
repeat the rule.

### 11.4 Paths and files

Reads stay inside the caller's own home and scratch (the site's `paths`) and job folders.
Paths must be clean and absolute (or `~/...`); every component is checked before and after
symlinks are resolved on the cluster; log paths come from the job's accounting record.
Hidden entries (any component starting with `.`) and credential-like names (`id_*`,
`*.pem`, `*.key`, `*.p12`, `*.pfx`, `*.env`, names containing credential, secret, token or
password) are refused for reading and left out of listings, inside job folders too, and a
whole-folder download skips them. A job folder that is hidden, credential-like or outside
the allowed roots is refused. Downloads unpack regular files only (no absolute paths, `..`
or links) and never overwrite.

### 11.5 Redaction and limits

Tokens and secrets are redacted from every answer and from the audit log: NAME=value
assignments for TOKEN/SECRET/PASSWORD/KEY names, private key blocks, `SLURM_JWT`, bearer
headers, Google, AWS, GitHub, Slack and OpenAI-style keys, OAuth tokens, JWTs, signed URL
signatures and credentials embedded in URLs. Outputs are paged rather than cut off; a page
is capped at 64,000 characters at a line boundary. Calls are rate-limited per process
(`limits.calls_per_min`, default 60).

### 11.6 Audit

Every call, CLI or MCP, appends one JSON line: time, caller (local OS user), client, tool,
redacted arguments, decision (allowed, denied, error, rate_limited), redacted commands,
result size and duration. The file is mode 0600 under `~/.local/share/clusterwand/`;
`audit_path: "-"` writes to stderr (stdout carries the MCP stdio stream).

### 11.7 Load

Short-TTL caches, the rate limit, one ControlMaster connection, no background polling and
no compute on the login node. On `cloud_nodes` sites `waste_report` and `health` treat
powered-down nodes as free.

## 12. Configuration

`~/.config/clusterwand/config.yaml` (XDG honoured; `--config PATH` overrides;
`clusterwand config path` prints it). `clusterwand config init` writes a commented starter
like this one, and `config show` prints the effective values:

```yaml
site: ursa-major          # or site_file: ~/.config/clusterwand/mysite.yaml
backend: ssh              # ssh | fixture (tests)
ssh:
  host: mycluster         # any host or alias your own ssh reaches without a prompt
  control_persist: 900
  connect_timeout: 30
# catalog_path: /path/to/catalog.json    # the site profile sets these; override here
# log_roots: ["/home/{user}/"]
# env_check_partition: debug
show_cost: true
# show_cost_tiers: [R2]                  # tiers that see cost figures (unset: all)
# usd_per_node_hour: {mypartition: 1.50}
# allocation: {person_usd: 200, lab: {id: mylab, name: My Lab, allocation_usd: 500, members: [alice, bob]}}
tiers: [R1]               # R1, R2, A1
caps: {max_nodes: 4, max_hours: 24, max_cost_usd_per_job: 25, max_cost_usd_per_day: 50,
       max_submits_per_day: 20, confirm_ttl_minutes: 10}
# SU sites also: caps.max_su_per_job, caps.max_su_per_day
results_dir: ~/clusterwand-results
jobs_root: clusterwand-jobs
state_path: ~/.local/share/clusterwand/a1.json
cache_ttl: {queue: 20s, nodes: 30s, acct: 60s, catalog: 1h, modules: 1h}
limits: {log_lines: 200, list_rows: 200, script_bytes: 65536, untrusted_chars: 16000, calls_per_min: 60}
audit_path: ~/.local/share/clusterwand/audit.jsonl
```

With no `limits` block the built-in `log_lines` default is 1000. `clusterwand doctor`
checks the config, SSH and the catalog. MCP client setup: [CLIENTS.md](CLIENTS.md).

## 13. Testing

- Fixtures: recorded real Slurm output (JSON and text; Slurm 25.11 and 21.08), anonymised
  (`scripts/make_fixtures.py`, `scripts/make_text_fixtures.py`); CI fails if a real
  username, uid or internal IP appears. Core runs against them without a cluster
  (`backend: fixture`); the fixture prints its recorded JSON answers as the text commands
  would, and `TestTextMatchesJSON{Queue,Acct,Nodes}` compare every field.
- Golden tests: `TestGoldenUrsaMajor` (every MCP tool, prompt and resource, and every CLI
  text renderer) keeps answers byte-identical across refactors; `TestGoldenUrsaMajorJSON`
  and `TestGoldenTextMatchesJSON` cover the JSON path; `TestGoldenSecondSite` runs the same
  calls against a synthetic SU-charged site (`testdata/sites/bigcluster`) and fails on any
  Ursa string or dollar figure. Regenerate with `GOLDEN_UPDATE=1`; every change is a
  reviewed diff.
- Rules: one sample per diagnosis rule in `TestEachLogRuleFires`.
- Policy: tier denials, token binding, single use and expiry, the cross-process lock,
  redaction, caps (arrays included), path and hidden-file rules, injection strings in job
  names and logs, cost visibility in answers and tool descriptions, `TestNoShellTool`,
  `TestAllowListIsClosed`.
- `make check` (gofmt, vet, `go test -race`, build) before every commit; CI runs it on every
  PR. `.github/workflows/slurm.yml` (optional) runs the text reads against Slurm 21.08.6 in
  a container (`go test -tags slurmlive -run Live`).
- `scripts/mutation_check.sh` disables each safety guard in turn and requires a test to
  fail; a guard whose pattern no longer applies is reported BROKEN and fails the run. New
  safety code gets a guard; guards are removed only with the code they protect.
- Live checks against a cluster are read-only (`doctor`, `status`, `jobs`, `job explain`)
  except jobs submitted through the two-step flow. The F4 campaign on Ursa Major is in
  [LIVE_CAMPAIGN_F4.md](LIVE_CAMPAIGN_F4.md).

## 14. Roadmap

| Phase | What | Status |
|---|---|---|
| F0 | Fork, rename, SSH only, docs, CI | done, v0.1.0 |
| F1 | Site profiles and charge models; golden tests | done, v0.2.0 |
| F2 | Plain-text Slurm adapter; lab-locked partitions | done, v0.3.0 |
| F3 | `discover` and host-key pinning | done, v0.4.0 |
| F4 | Live campaign on Ursa Major over plain SSH | done, v0.5.0 (review fixes v0.5.1) |
| Cleanup | Hosted-mode plumbing removed from core; this spec | done, v0.6.0 |
| Next | A second real cluster; multi-cluster configs | not started |

## 15. History

ClusterWand inherits ursa-bifrost's design (spec Drafts 1-8, releases v0.1.0 to v0.10.1,
2026-10-01 to 2026-10-27): the hosted server with Google sign-in, the IAP/OS Login backend,
Cloud Storage staging, the ursa-agent dashboard, program clients, the server-wide shared
cache and in-flight caps, allocation framing, and the measurements and live campaigns
behind them. That record is kept unchanged in
[history/URSA_BIFROST_SPEC_v0.10.1.md](history/URSA_BIFROST_SPEC_v0.10.1.md);
[CLOUD_PLAN.md](CLOUD_PLAN.md) and [SCALING.md](SCALING.md) are the matching ursa-bifrost
planning documents. None of the hosted code is in ClusterWand.

## Change log

| Date | Version | Change |
|---|---|---|
| 2026-10-29 | v0.6.0 | Cleanup. Removed hosted-mode plumbing that nothing in the CLI or the stdio MCP server reached: the cross-user shared cache and single-flight, the per-caller in-flight cap and server-wide accounting gate, program-client audit naming and the own-caps cost override, sealed A1 state, `Command.public`, the remote-download refusal. `cluster_status` and `partitions` descriptions no longer advertise cost the caller cannot see (site keys `tool_cluster_status_nocost`, `tool_partitions_nocost`). This spec rewritten for ClusterWand; the inherited spec moved to docs/history. 14 mutation guards for removed code dropped, 2 new |
| 2026-10-29 | v0.5.1 | Review fixes: job arrays count every task against the caps; uncountable arrays and hetjobs refused; `job_results` resolves and checks the job folder itself; audit `"-"` goes to stderr. 10 new mutation guards |
| 2026-10-29 | v0.5.0 | Live campaign on Ursa Major over plain SSH; `job_results` honours the hidden/credential rule; new `build-failure` rule; confirm-token error wording. 3 new mutation guards |
| 2026-10-29 | v0.4.0 | `clusterwand discover` (section 8); host-key pinning; local `catalog_file` with a draft gate; `--record` anonymised fixtures; `--compare`. 23 new mutation guards |
| 2026-10-28 | v0.3.0 | Plain-text Slurm reads as the primary path, JSON behind `slurm.json`; Slurm 21.08 fallbacks; output-path expansion; lab-locked partitions. 30 new mutation guards |
| 2026-10-28 | v0.2.0 | Site profiles; charge models `usd_per_node_hour`, `su_per_core_hour`, `none`; golden tests; synthetic second site. 19 new mutation guards |
| 2026-10-28 | v0.1.0 | Forked from ursa-bifrost v0.10.1 (77cb4cd) as ClusterWand: renamed module, binary, paths and names; SSH only (hosted server, OAuth, IAP backend, Cloud Storage staging, ursa-agent and the Cloud Run deploy removed); docs/SSH.md |

Earlier versions: [history/URSA_BIFROST_SPEC_v0.10.1.md](history/URSA_BIFROST_SPEC_v0.10.1.md).
