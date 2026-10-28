# SSH setup

ClusterWand runs on your own machine and reaches the cluster exactly the way you do: by
running OpenSSH (`ssh`) with your account, your keys and your `~/.ssh/config`. There is no
server, no sign-in and no credential of its own. If this works with no prompt:

```bash
ssh -o BatchMode=yes mycluster true && echo ok
```

then ClusterWand works too. Point it at the same alias:

```yaml
# ~/.config/clusterwand/config.yaml
backend: ssh
ssh:
  host: mycluster          # any ~/.ssh/config alias or user@host
  control_persist: 900     # seconds the shared connection stays open when idle (default 900)
  connect_timeout: 30      # seconds (default 30)
```

`clusterwand doctor` checks the connection and tells you what failed.

## What ClusterWand passes to ssh

Every remote command runs as

```text
ssh -o ControlMaster=auto -o ControlPath=$XDG_RUNTIME_DIR/cw-%C -o ControlPersist=<control_persist>
    -o BatchMode=yes -o ServerAliveInterval=30 -o ConnectTimeout=<connect_timeout> -T
    -- <ssh.host> '<fixed command line>'
```

(`$TMPDIR` when `XDG_RUNTIME_DIR` is unset.) Everything else (user name, port, keys, jump
hosts, proxy commands, host keys) comes from your own `~/.ssh/config`. The command line is
built only by the allow-listed constructors in `internal/backend/`; nothing a model sends is
run as shell.

Two consequences:

- **`BatchMode=yes`**: ssh never asks for a password, passphrase or MFA code. If it would
  have to ask, the call fails with `unreachable` instead of hanging. Get a working
  connection first (below), then ClusterWand reuses it.
- **One shared connection**: the first call opens a master connection; later calls reuse it
  (about 0.3 s per command once warm) until it has been idle for `control_persist` seconds.

## Keys and the agent

The simplest setup is a key loaded into an agent:

```bash
ssh-keygen -t ed25519 -C "you@laptop"          # once; use a passphrase
ssh-copy-id -i ~/.ssh/id_ed25519.pub you@login.cluster.example.edu   # or your site's key upload page
ssh-add ~/.ssh/id_ed25519                      # each login session (macOS: add --apple-use-keychain)
```

```sshconfig
# ~/.ssh/config
Host mycluster
    HostName login.cluster.example.edu
    User you
    IdentityFile ~/.ssh/id_ed25519
    IdentitiesOnly yes
    AddKeysToAgent yes
    # macOS: keep the passphrase in the keychain
    UseKeychain yes
```

ClusterWand never reads your keys; ssh and the agent do. Accept the login node's host key
once by hand (`ssh mycluster true`), because `BatchMode` will not prompt for it.

## Jump hosts (ProxyJump)

If the login node is only reachable through a gateway, describe it in `~/.ssh/config` and
use the inner alias as `ssh.host`:

```sshconfig
Host cluster-gw
    HostName gateway.example.edu
    User you

Host mycluster
    HostName login01.internal
    User you
    ProxyJump cluster-gw
```

Any `ProxyCommand` works the same way (a VPN-like tunnel, a site's bastion tool): it is
only the network path, and ClusterWand does not know or care.

## ControlMaster: sharing one connection

ClusterWand sets its own `ControlMaster`/`ControlPath`, so it multiplexes even if your
config does not. You can also enable multiplexing for your own interactive logins:

```sshconfig
Host mycluster
    ControlMaster auto
    ControlPath ~/.ssh/cm-%C
    ControlPersist 1h
```

The two masters are separate sockets (`cw-%C` versus yours), so ClusterWand opens its own
connection. To check or close ClusterWand's master:

```bash
ssh -O check -o ControlPath="${XDG_RUNTIME_DIR:-$TMPDIR}/cw-%C" mycluster
ssh -O exit  -o ControlPath="${XDG_RUNTIME_DIR:-$TMPDIR}/cw-%C" mycluster
```

OpenSSH servers allow 10 sessions per connection by default (`MaxSessions`). v0.1.0 does not
queue its own calls below that, so a client that fires more than about 10 calls at once may see
some fail with `unreachable` (`open failed`); retry them. A per-connection cap is planned.

## MFA and Duo: once per ControlPersist window

Many clusters ask for a second factor (Duo push, TOTP) at every new connection. Because
`BatchMode` cannot answer prompts, open ClusterWand's master connection yourself once,
answer the prompt, and ClusterWand rides on it:

```bash
# answer the MFA prompt once; the master then stays open in the background
ssh -o ControlMaster=auto -o ControlPath="${XDG_RUNTIME_DIR:-$TMPDIR}/cw-%C" \
    -o ControlPersist=900 -fN mycluster
clusterwand status          # reuses it, no prompt
```

The connection lives until it has been idle for `ControlPersist` seconds (match
`ssh.control_persist`; raise both for a long working session, for example `8h` and
`28800`). When it closes, calls fail with `unreachable`; run the command above again.
Your site's policy decides how long a session may stay open; stay within it.

If your site allows it, a key with the agent avoids MFA prompts entirely; ask your cluster
administrators rather than working around their policy.

## Troubleshooting

| Symptom | Fix |
|---|---|
| `unreachable: Permission denied (publickey...)` | The key is not loaded (`ssh-add -l`) or not on the cluster. Check `ssh -o BatchMode=yes mycluster true`. |
| `unreachable: Host key verification failed` | Accept the host key once by hand: `ssh mycluster true`. |
| `unreachable` after it worked earlier | The master connection closed (idle, network change, MFA window). Open it again (MFA section) or just retry with a key. |
| Calls hang about 30 s then fail | Network path: check the jump host or VPN; lower `ssh.connect_timeout` if you prefer failing fast. |
| `ControlPath too long` | Set `XDG_RUNTIME_DIR` to a short directory you own (macOS `$TMPDIR` paths are long but usually fit `cw-%C`). |
| Works in a terminal, not from an MCP client | The client may start `clusterwand` without your agent socket. Start the client from a shell that has `SSH_AUTH_SOCK`, or use the MFA-style pre-opened master. |
| `ssh -vvv mycluster true` looks fine but ClusterWand fails | Run `clusterwand doctor --json` and read the failing step. |

ClusterWand never edits `~/.ssh/config`, never installs anything on the cluster, and
needs nothing beyond a normal user login there.
