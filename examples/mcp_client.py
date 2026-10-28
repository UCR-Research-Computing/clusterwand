#!/usr/bin/env python3
"""Minimal script client for ClusterWand's stdio MCP server.

  python3 examples/mcp_client.py tools                       # list tools
  python3 examples/mcp_client.py call cluster_status         # call a tool
  python3 examples/mcp_client.py call job_explain '{"job_id": "236"}'

Environment:
  CLUSTERWAND      binary to run (default: clusterwand on PATH)
  CLUSTERWAND_ARGS extra arguments, e.g. "--config /path/config.yaml"

It starts `clusterwand mcp`, which reaches the cluster with your own SSH login, and
speaks JSON-RPC over stdin/stdout. Standard library only; a readable reference for
what an MCP client does. For plain scripting, `clusterwand <command> --json` is simpler.
"""

from __future__ import annotations

import json
import os
import shlex
import subprocess
import sys

BIN = os.environ.get("CLUSTERWAND", "clusterwand")
ARGS = shlex.split(os.environ.get("CLUSTERWAND_ARGS", ""))


class Client:
    def __init__(self) -> None:
        self.p = subprocess.Popen(
            [BIN, "mcp", *ARGS], stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True
        )
        self.next_id = 0
        self.request(
            "initialize",
            {
                "protocolVersion": "2025-06-18",
                "capabilities": {},
                "clientInfo": {"name": "example-client", "version": "1"},
            },
        )
        self.notify("notifications/initialized")

    def notify(self, method: str) -> None:
        self._send({"jsonrpc": "2.0", "method": method})

    def request(self, method: str, params: dict | None = None) -> dict:
        self.next_id += 1
        msg = {"jsonrpc": "2.0", "id": self.next_id, "method": method}
        if params is not None:
            msg["params"] = params
        self._send(msg)
        while True:
            line = self.p.stdout.readline()
            if not line:
                raise SystemExit("server closed the connection")
            m = json.loads(line)
            if m.get("id") == self.next_id:
                if "error" in m:
                    raise SystemExit(f"error: {m['error']}")
                return m["result"]

    def _send(self, msg: dict) -> None:
        self.p.stdin.write(json.dumps(msg) + "\n")
        self.p.stdin.flush()

    def close(self) -> None:
        self.p.stdin.close()
        self.p.wait(timeout=10)


def main() -> None:
    if len(sys.argv) < 2 or sys.argv[1] not in ("tools", "call"):
        raise SystemExit(__doc__)
    c = Client()
    try:
        if sys.argv[1] == "tools":
            for t in c.request("tools/list")["tools"]:
                print(f"{t['name']:24} {t.get('description', '')[:90]}")
        else:
            args = json.loads(sys.argv[3]) if len(sys.argv) > 3 else {}
            r = c.request("tools/call", {"name": sys.argv[2], "arguments": args})
            out = r.get("structuredContent") or r.get("content")
            print(json.dumps(out, indent=2))
            if r.get("isError"):
                sys.exit(1)
    finally:
        c.close()


if __name__ == "__main__":
    main()
