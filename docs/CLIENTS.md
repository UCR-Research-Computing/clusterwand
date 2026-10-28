# Connecting clients

ClusterWand runs on your own machine as a local (stdio) MCP server: your AI client starts
`clusterwand mcp` as a child process and talks to it over stdin/stdout. There is no URL, no
sign-in and no hosted server. ClusterWand reaches the cluster with your own SSH login
(`ssh.host` in `~/.config/clusterwand/config.yaml`; see [SSH.md](SSH.md)).

Before connecting a client, check the binary on its own:

```bash
curl -fsSL https://raw.githubusercontent.com/UCR-Research-Computing/clusterwand/main/scripts/get.sh | sh
clusterwand config init        # then set ssh.host
clusterwand doctor             # config, SSH and catalog
```

The server name is your choice (`cluster` below); the tools appear as `cluster_status`,
`job_explain`, and so on. If `clusterwand` is not on the PATH your client sees, use the
full path (for example `~/.local/bin/clusterwand`). To use a config other than the default,
add `--config /path/config.yaml` after `mcp`.

## Contents

- [Hermes Agent](#hermes-agent)
- [Claude Code](#claude-code)
- [Claude Desktop](#claude-desktop)
- [OpenAI Codex CLI](#openai-codex-cli)
- [OpenCode](#opencode)
- [Gemini CLI](#gemini-cli)
- [GitHub Copilot (VS Code)](#github-copilot-vs-code)
- [Scripts](#scripts)
- [Any other client](#any-other-client)
- [Troubleshooting](#troubleshooting)

---

## Hermes Agent

```bash
hermes mcp add cluster --command clusterwand --args mcp
hermes mcp test cluster
```

Start a new session to get the tools (`mcp__cluster__cluster_status`, ...).

Approval prompts: Hermes asks before every tool on an untrusted server. ClusterWand's
read tools are marked `readOnlyHint: true` and its act tools need a second confirm call
anyway, so you can skip the prompts with:

```bash
hermes config set mcp_servers.cluster.trust full
```

## Claude Code

```bash
claude mcp add cluster -- clusterwand mcp
```

Add `-s user` to make it available in every project (default: this project only). Check
with `/mcp` inside Claude Code.

## Claude Desktop

In `claude_desktop_config.json`:

```json
{ "mcpServers": { "cluster": { "command": "clusterwand", "args": ["mcp"] } } }
```

Claude Desktop starts apps with a minimal PATH; use the full path to `clusterwand` if it
is not found. (claude.ai in the browser only supports remote servers, which ClusterWand
does not provide.)

## OpenAI Codex CLI

```bash
codex mcp add cluster -- clusterwand mcp
codex mcp list
```

Or in `~/.codex/config.toml`:

```toml
[mcp_servers.cluster]
command = "clusterwand"
args = ["mcp"]
```

Older Codex releases (the 0.1.x TypeScript CLI) have no `mcp` command; update with
`npm install -g @openai/codex`.

## OpenCode

In `~/.config/opencode/opencode.jsonc`:

```jsonc
{
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    "cluster": { "type": "local", "command": ["clusterwand", "mcp"] }
  }
}
```

## Gemini CLI

```bash
gemini mcp add -s user cluster clusterwand mcp
```

Settings land in `~/.gemini/settings.json`:

```json
{ "mcpServers": { "cluster": { "command": "clusterwand", "args": ["mcp"] } } }
```

## GitHub Copilot (VS Code)

Copilot Chat uses MCP tools in agent mode. Add a workspace file `.vscode/mcp.json`
(or the same block under `"mcp"` in your user settings):

```json
{ "servers": { "cluster": { "type": "stdio", "command": "clusterwand", "args": ["mcp"] } } }
```

Open Copilot Chat, switch to **Agent**, and enable the `cluster` tools in the tools picker.

## Scripts

Every tool is also a CLI command with `--json`, which is the simplest thing to script:

```bash
clusterwand status --json
clusterwand job explain 236 --json
clusterwand check job.sbatch --json
```

To drive the MCP server itself, [`examples/mcp_client.py`](../examples/mcp_client.py) is a
complete stdio client in one file, standard library only:

```bash
python3 examples/mcp_client.py tools
python3 examples/mcp_client.py call cluster_status
python3 examples/mcp_client.py call job_explain '{"job_id": "236"}'
CLUSTERWAND_ARGS="--config /path/config.yaml" python3 examples/mcp_client.py tools
```

With the official Python SDK (`pip install mcp`):

```python
import asyncio
from mcp import ClientSession, StdioServerParameters
from mcp.client.stdio import stdio_client

async def main():
    params = StdioServerParameters(command="clusterwand", args=["mcp"])
    async with stdio_client(params) as (read, write):
        async with ClientSession(read, write) as s:
            await s.initialize()
            r = await s.call_tool("partitions", {})
            print(r.content[0].text)

asyncio.run(main())
```

## Any other client

Configure a local / stdio server with command `clusterwand` and argument `mcp`. The
server speaks MCP JSON-RPC over stdin/stdout.

## Troubleshooting

| Symptom | Cause and fix |
|---|---|
| Client says the server failed to start | `clusterwand` is not on the client's PATH. Use the full path, and run `clusterwand doctor` in a terminal. |
| Every tool returns `unreachable` | SSH from the client's environment does not work without a prompt. See [SSH.md](SSH.md) (agent socket, MFA window, host key). |
| A tool you expected is missing | Tools follow the tiers in your config: R1 read, R2 staff, A1 submit/cancel. The tool list only shows what you may call. |
| `rate limited` | Over the call budget (`limits.calls_per_min`, default 60 a minute). Wait a moment. |
| Some calls in a large parallel burst fail with `unreachable` | The login node's SSH `MaxSessions` (default 10 per connection). Keep parallelism at 8 or less, or retry. |
| `job_submit` returns a token, nothing runs | That is the design: show the plan, then call `job_submit_confirm` with the token after you approve. Tokens expire in 10 minutes. |
| Every read asks for approval (Hermes) | Hermes treats untrusted servers' tools as writes; see the Hermes section for `trust full`. |
