# Connecting clients

ClusterWand runs two ways. Pick one, then follow the section for your client.

| | Local (start here) | Hosted |
|---|---|---|
| What runs | the `clusterwand` binary on your machine | `clusterwand serve` (today: Google sign-in, IAP backend; see `sites/ursa-major/`) |
| URL / command | `clusterwand mcp` (stdio) | `https://YOUR-SERVER/mcp` |
| Sign-in | none: uses your own SSH login (`ssh.host`) | Google, accounts in your Workspace domain listed in `users.yaml` |
| Install | `curl -fsSL https://raw.githubusercontent.com/UCR-Research-Computing/clusterwand/main/scripts/get.sh \| sh` | nothing for users |
| Works from | your machine only | any MCP client, any machine |

Each section below gives the hosted command first and the local (stdio) variant after it.
For local use the server name is your choice (`cluster` here). The hosted server speaks
**Streamable HTTP** at `/mcp` and does **OAuth 2.1** with dynamic client registration
(RFC 7591), PKCE and discovery (RFC 9728), so any client that supports remote MCP servers
signs you in by itself.

> Below, `URL` means your server's address, `https://YOUR-SERVER/mcp` (your cluster administrator gives it to you).
> The hosted commands were checked against ursa-bifrost's live server on 2026-10-02 (same code).

## Contents

- [Hermes Agent](#hermes-agent)
- [Claude Code](#claude-code)
- [Claude Desktop and claude.ai](#claude-desktop-and-claudeai)
- [OpenAI Codex CLI](#openai-codex-cli)
- [OpenCode](#opencode)
- [Gemini CLI](#gemini-cli)
- [GitHub Copilot (VS Code)](#github-copilot-vs-code)
- [OpenAI API (Responses API)](#openai-api-responses-api)
- [Scripts (Python, curl)](#scripts-python-curl)
- [Any other client](#any-other-client)
- [Troubleshooting](#troubleshooting)

---

## Hermes Agent

```bash
hermes mcp add cluster --url https://YOUR-SERVER/mcp --auth oauth
hermes mcp test cluster
```

`add` opens the browser to sign in and asks which tools to enable. Tokens are kept in
`~/.hermes/mcp-tokens/` and refresh on their own. Start a new session to get the tools
(`mcp__cluster__cluster_status`, ...).

Approval prompts: Hermes asks before every tool on an untrusted server. clusterwand's
read tools are marked `readOnlyHint: true` and its act tools need a second confirm call
anyway, so you can skip the prompts with:

```bash
hermes config set mcp_servers.cluster.trust full
```

Local instead: `hermes mcp add cluster --command clusterwand --args mcp`.

## Claude Code

```bash
claude mcp add --transport http cluster https://YOUR-SERVER/mcp
```

Then, inside Claude Code, run `/mcp`, pick `cluster`, choose **Authenticate**, and sign in.
Add `-s user` to make it available in every project (default: this project only).

Local instead: `claude mcp add cluster -- clusterwand mcp`.

## Claude Desktop and claude.ai

Settings -> Connectors -> **Add custom connector**:

- Name: `HPC cluster`
- URL: `https://YOUR-SERVER/mcp`

Claude opens the sign-in page; pick your organization account. On a Team or Enterprise plan
an owner may need to allow custom connectors first.

Local instead (Desktop only), in `claude_desktop_config.json`:

```json
{ "mcpServers": { "cluster": { "command": "clusterwand", "args": ["mcp"] } } }
```

## OpenAI Codex CLI

```bash
codex mcp add cluster --url https://YOUR-SERVER/mcp
```

Codex (0.160+) sees that the server supports OAuth and opens the sign-in right away.
To sign in again later: `codex mcp login cluster` (`--no-browser` prints the URL instead).
Check with `codex mcp list`.

Or in `~/.codex/config.toml`:

```toml
[mcp_servers.cluster]
url = "https://YOUR-SERVER/mcp"
```

then `codex mcp login cluster`. Local instead: `codex mcp add cluster -- clusterwand mcp`.
Older Codex releases (the 0.1.x TypeScript CLI) have no `mcp` command; update with
`npm install -g @openai/codex`.

## OpenCode

```bash
opencode mcp add cluster --url https://YOUR-SERVER/mcp
opencode mcp auth cluster
```

That writes `~/.config/opencode/opencode.jsonc`:

```jsonc
{
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    "cluster": { "type": "remote", "url": "https://YOUR-SERVER/mcp" }
  }
}
```

OpenCode registers itself with the server and stores tokens in
`~/.local/share/opencode/mcp-auth.json`. Local instead:
`"cluster": { "type": "local", "command": ["clusterwand", "mcp"] }`.

## Gemini CLI

```bash
gemini mcp add -s user -t http cluster https://YOUR-SERVER/mcp
```

Start `gemini`, then run `/mcp auth cluster` to sign in. Settings land in
`~/.gemini/settings.json`:

```json
{ "mcpServers": { "cluster": { "url": "https://YOUR-SERVER/mcp", "type": "http" } } }
```

Local instead: `gemini mcp add -s user cluster clusterwand mcp`.

## GitHub Copilot (VS Code)

Copilot Chat uses MCP tools in agent mode. Add a workspace file `.vscode/mcp.json`
(or the same block under `"mcp"` in your user settings):

```json
{ "servers": { "cluster": { "type": "stdio", "command": "clusterwand", "args": ["mcp"] } } }
```

Hosted instead: `{ "servers": { "cluster": { "type": "http", "url": "https://YOUR-SERVER/mcp" } } }`;
VS Code runs the OAuth sign-in when the server asks. Open Copilot Chat, switch to
**Agent**, and enable the `cluster` tools in the tools picker.

## OpenAI API (Responses API)

The Responses API can call a remote MCP server itself. OpenAI's servers make the calls,
so you pass a bearer token you already have (get one with the script below:
`python3 examples/mcp_client.py login`, then read `access_token` from the token file;
it lasts an hour).

```python
import json, os
from openai import OpenAI

token = json.load(open(os.path.expanduser(
    "~/.config/mcp-client/YOUR-SERVER.json")))["access_token"]

client = OpenAI()
resp = client.responses.create(
    model="gpt-5",
    tools=[{
        "type": "mcp",
        "server_label": "cluster",
        "server_url": "https://YOUR-SERVER/mcp",
        "authorization": token,
        "require_approval": "never",
        "allowed_tools": ["cluster_status", "partitions", "job_explain", "script_check"],
    }],
    input="Is anything running on the cluster right now, and what does it cost per hour?",
)
print(resp.output_text)
```

Keep `allowed_tools` to read tools when `require_approval` is `never`. The act tools
(`job_submit`, `job_cancel`, ...) still need their confirm call, but a model running
unattended should not be the one making it.

## Scripts (Python, curl)

[`examples/mcp_client.py`](../examples/mcp_client.py) is a complete client in one file,
standard library only:

```bash
python3 examples/mcp_client.py login            # browser sign-in, tokens saved 0600
python3 examples/mcp_client.py tools
python3 examples/mcp_client.py call cluster_status
python3 examples/mcp_client.py call job_explain '{"job_id": "236"}'
python3 examples/mcp_client.py call script_check "$(jq -n --rawfile s job.sbatch '{script: $s}')"
```

The server is stateless, so after login a tool call is one HTTP POST:

```bash
TOKEN=$(jq -r .access_token ~/.config/mcp-client/YOUR-SERVER.json)
curl -s https://YOUR-SERVER/mcp \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"cluster_status","arguments":{}}}'
```

With the official Python SDK (`pip install mcp`):

```python
import asyncio
from mcp import ClientSession
from mcp.client.streamable_http import streamablehttp_client

async def main(token: str):
    async with streamablehttp_client(
        "https://YOUR-SERVER/mcp",
        headers={"Authorization": f"Bearer {token}"},
    ) as (read, write, _):
        async with ClientSession(read, write) as s:
            await s.initialize()
            r = await s.call_tool("partitions", {})
            print(r.content[0].text)
```

For scripts with no browser, the local binary is simpler: `clusterwand status --json`,
`clusterwand job explain 236 --json`. Every CLI command takes `--json`.

## Programs (pre-registered clients)

A long-running program (a dashboard, Ultra) can use a client id listed under `clients:` in
the server's `users.yaml` instead of registering one. It signs in as a person like any
client, but its tools are that person's tiers capped by the program's `tiers` (a ceiling,
never a grant), and it has its own `calls_per_min`, separate from the person's chat
clients. Use the listed `client_id` and one of its redirect URIs (loopback ports may
differ); skip `/register`. `GET /whoami` shows `program`, the capped `tiers` and
`calls_per_min`. Refresh tokens rotate: a program that refreshes from several threads must
lock around the refresh. Audit records read `program:<id>/mcp:<client name>`.

## Any other client

Point it at `URL` with transport "Streamable HTTP" (sometimes called "http"; not "SSE")
and let it do OAuth. Discovery starts from the `401` on `/mcp`, whose
`WWW-Authenticate` header points to `/.well-known/oauth-protected-resource`. Endpoints:
`/authorize`, `/token`, `/register`, `/revoke`. Redirect URIs must be `https` or an
`http` loopback address (`127.0.0.1`, `localhost`, `[::1]`; any port).

## Troubleshooting

| Symptom | Cause and fix |
|---|---|
| Sign-in page says your account is not allowed | Only accounts listed in the server's `users.yaml` get in. Ask your cluster's administrator to add you. |
| `401` on every call after it worked | Access tokens last an hour and refresh tokens 30 days; refresh tokens rotate, so two clients sharing one token file break each other. Sign in again per client. |
| Client says "SSE" or "invalid transport" | Choose Streamable HTTP / `http`. clusterwand does not serve SSE. |
| A tool you expected is missing | Tools follow your tier: R1 read, R2 staff, A1 submit/cancel. The tool list only shows what you may call. Through a program client, the program's tier ceiling also applies. |
| `rate limited` | Over the call budget (default 60 a minute per person; a program client has its own). Wait a moment. |
| `busy: 16 of your calls are still running` | More than 16 of your calls in flight at once. Wait for one to finish; keep parallelism at 8 or less (the SSH sessions per person). |
| `busy: accounting is busy for everyone` | Every accounting slot (4 server-wide) stayed taken for the whole command timeout. Try again shortly. |
| `job_submit` returns a token, nothing runs | That is the design: show the plan, then call `job_submit_confirm` with the token after you approve. Tokens expire in 10 minutes. |
| Every read asks for approval (Hermes) | Hermes treats untrusted servers' tools as writes; see the Hermes section for `trust full`. |
