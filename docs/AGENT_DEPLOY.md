# muse-console — Muse Sandbox VM Deployment Guide

**Audience:** strictly for the Muse Agent deploying muse-console inside a Muse sandbox VM and exposing it to the internet for the user. Not intended for human readers, and applies only to Muse sandbox VMs.

> **GitHub repository / release binaries:** `https://github.com/lxrite/muse-console`  
> *(until releases are published, build from source per §4.)*

---

## 0. What you are deploying (30-second brief)

- **One static Go binary** (`go build`, no runtime dependencies, templates and web assets embedded).
- **Environment:** Muse sandbox VM (Linux, user `hatch`). The VM is ephemeral: when idle it may shut down or be destroyed at any time — **only `/home/hatch` persists**. Everything (binaries, configs, scripts) must live under `/home/hatch`.
- Listener on the sandbox VM:
  - `addr` (default `127.0.0.1:8080`) — **private console** behind username/password login: status dashboard `/`, web terminal `/term`, file browser `/files`.
- State: `timeline.jsonl` (heartbeat history) under `data_dir`; configuration in TOML (`config.toml`, must stay `chmod 600` — it holds plaintext passwords).
- **Remote-access pattern:** the sandbox VM has **no public IP** and cannot be reached directly. Browsers reach it through a **rathole TCP tunnel** to a relay server that *does* have a public IP: `browser → relay public port → tunnel → muse-console on the sandbox VM`. Setting up both ends of that tunnel and the platform keepalive task is the main job of this guide.

## 1. Golden rule: explain WHY before asking for anything

**Do not ask the user for relay / rathole details until you have explained why they are needed.** Suggested wording (adapt to the user's language):

> muse-console runs inside your Muse sandbox VM, which has no public IP and cannot be reached directly from the internet. To open the console in your browser from anywhere, we set up an encrypted tunnel (rathole) from this sandbox VM to a relay server that has a public IP. Your browser → relay public port → tunnel → muse-console inside this VM.
>
> That's why I need a relay server: one you already run, or a small VPS. I'll need its public address and the tunnel credentials.

Only proceed once the user confirms they have (or will provide) a relay. If the user does not have a publicly reachable server, clearly explain that one is required for internet access and stop before making changes.

## 2. Information to collect from the user

Ask for each item explicitly. **Do not guess values, do not invent tokens.**

| # | Item | Example | Notes |
|---|------|---------|-------|
| 1 | Relay public IP or hostname | `203.0.113.10` | Must be reachable from the sandbox VM |
| 2 | rathole server control port | `12333` | The `bind_addr` port in the *server's* rathole config |
| 3 | rathole service token | random string | Shared secret for rathole, **identical** on client and server. If the user has none, generate one (`openssl rand -hex 16`) and hand it to them for the server side |
| 4 | Public port for muse-console | `12080` | → forwards to muse-console's `addr` (login wall — never expose without auth) |

**Do NOT ask the user about proxies.** Whether outbound traffic needs an HTTP CONNECT proxy is detected automatically from the sandbox environment — see §7. Never ask the user for proxy host/port.

Hand the user the **server-side snippet** (§6) and ask them to add it to their rathole server config and restart the server. Do not treat the tunnel as working until the end-to-end checks in §8 pass.

## 3. Prerequisites

### 3.1 External relay server (user-provided)

Because Muse sandbox VMs have no public IP, internet exposure strictly depends on reverse tunneling:
- **Publicly accessible server / VPS:** The user **MUST** have a server with a public IP or resolvable domain name to act as the relay.
- **Permission to run rathole server & open ports:** The user must be able to run `rathole` on that server and ensure firewalls/cloud security groups permit inbound traffic on both the control port (e.g. `12333`) and the console forwarding port (e.g. `12080`).
- **If the user lacks a relay:** Inform them upfront that a public relay server is an indispensable prerequisite for web access. Do not proceed with sandbox setup until they can provide one.

### 3.2 Sandbox environment prerequisites

- All files, scripts, binaries, and configurations **MUST be placed under `/home/hatch`** (e.g. `/home/hatch/workspace/muse-console`). Any file outside `/home/hatch` will be permanently lost when the VM is destroyed.
- To build from source: Go toolchain ≥ 1.21 (module declares go 1.22).
- For the tunnel: a `rathole` client binary (match the **server's** rathole version: https://github.com/rapiz1/rathole/releases) and, only if §7 applies, `socat`. Keep them under `/home/hatch/bin` or the workspace.
- The muse-console port (`8080`) and the local bridge port (`12334`, if used) must be free.

## 4. Get the software

**Option A — prebuilt binary (recommended):**  
```bash
mkdir -p /home/hatch/workspace/muse-console && cd /home/hatch/workspace/muse-console
ARCH=$(uname -m)
[ "$ARCH" = "x86_64" ] && ARCH="amd64"
[ "$ARCH" = "aarch64" ] && ARCH="arm64"
curl -fsSL -o muse-console.tar.gz "https://github.com/lxrite/muse-console/releases/download/latest/muse-console-linux-${ARCH}.tar.gz"
tar -xzf muse-console.tar.gz
chmod +x muse-console
```

**Option B — build from source:**  
```bash
cd /home/hatch/workspace
git clone https://github.com/lxrite/muse-console.git
cd muse-console
go build -o muse-console .
go test ./...   # optional sanity check
```

## 5. Configure muse-console

Create `config.toml` under the application directory (`chmod 600`):

```toml
addr = "127.0.0.1:8080"
data_dir = "."
disk_path = "/home/hatch"    # dashboard shows filesystem usage of /home/hatch
files_root = "/"            # file browser root (jailed to this tree)

[[users]]
username = "admin"
password = "<6-digit-temp-password>"
```

CLI flags (`-addr`, `-data-dir`, `-disk-path`, `-files-root`, `-config`) override the file.

**Account setup (agent-driven):**
1. Ask the user what username and password they want for the console.
2. If they provide both, use them.
3. If they don't (or only give a username), use their username — or `admin` as default — with a random 6-digit temporary password: `TEMP=$(shuf -i 100000-999999 -n 1)`.
4. Write the credentials into `config.toml`; `chmod 600 config.toml`.
5. Start the service and verify (§8), **then** tell the user the account name and password in chat. If it is a temporary password, advise them to log in and change it via the `/change-password` page.

## 6. Configure the rathole tunnel

**Client** (`client.toml` under `/home/hatch/...`):

```toml
[client]
remote_addr = "127.0.0.1:12334"   # via socat bridge (§7), or "RELAY_IP:CONTROL_PORT" for direct TCP
default_token = "<token-from-§2>"

[client.services.muse-console]
type = "tcp"
local_addr = "127.0.0.1:8080"     # private console (login wall)
```

**Server** — give this snippet to the user to add to *their* rathole server config (service names and tokens must match the client; adapt field names to their rathole version):

```toml
[server.services.muse-console]
token = "<same-token>"
bind_addr = "0.0.0.0:12080"
```

## 7. Egress-proxy bridge (auto-detected — never ask the user)

**Do not ask the user for proxy details.** Detect them from the sandbox environment:

1. Read `http_proxy` / `https_proxy` (and the uppercase `HTTP_PROXY` / `HTTPS_PROXY`) from the environment. Format is usually `http://[user:password@]proxy-host:proxy-port`. The credentials in there are secrets: you may *use* them in commands, but **never print, log, or repeat them**.
2. Test direct connectivity first, with a short timeout:
   ```bash
   timeout 5 bash -c 'echo > /dev/tcp/RELAY_IP/CONTROL_PORT' && echo DIRECT_OK
   ```
3. If direct TCP works → no bridge needed; set `remote_addr = "RELAY_IP:CONTROL_PORT"` in `client.toml` and skip the rest.
4. If direct fails **and** a proxy env var is set → bridge it with socat, because rathole cannot speak the HTTP CONNECT proxy protocol itself:
   ```bash
   socat TCP-LISTEN:12334,bind=127.0.0.1,reuseaddr,fork \
         PROXY:<proxy-host>:<relay-ip>:<control-port>,proxyport=<proxy-port> &
   ```
   (If the proxy URL carries `user:password`, add `,proxyauth=<user>:<password>` to the `PROXY:` address.)  
   Keep the client's `remote_addr = "127.0.0.1:12334"` as in the §6 template.

In Muse sandbox VMs, outbound public internet traffic usually requires going through the egress proxy — without it, connections fail. So when proxy env vars are present, assume the bridge is required unless direct connectivity is positively proven by step 2.

## 8. Start and verify

Start order: muse-console → socat (if §7) → rathole client. (Long-term persistence in the sandbox is handled by the platform keepalive task in §9).

**Do not report success until every check passes:**

| Check | Expected |
|-------|----------|
| `curl -s -o /dev/null -w "%{http_code}" http://127.0.0.1:8080/` | `302` (redirect to `/login` = login wall healthy) |
| `curl -s -o /dev/null -w "%{http_code}" http://RELAY_IP:12080/` | `302` through the tunnel (login wall) |
| `ss -tln \| grep -E '8080\|12334'` | all LISTEN (as applicable) |

## 9. Ephemerality warning and keepalive task

**Before investing user effort, explain the sandbox VM lifecycle and the keepalive solution** (adapt to their language):

> Heads-up: Muse's sandbox VMs are ephemeral. When no agent task is running, the VM may be shut down or destroyed at any time — and when that happens, **only files under `/home/hatch` survive**.
>
> To keep muse-console running 24/7, we configure a platform scheduled task that wakes an agent every 5 minutes to verify and restart any missing processes (muse-console, socat bridge, rathole client). Waking the agent automatically powers the VM back on if it was shut down. With a 5-minute schedule the service stays online reliably (>99% uptime).

**Steps to configure:**

1. **Create the keepalive script under `/home/hatch/`:** Single-run and idempotent: it checks each service once and exits immediately. No `while`/`sleep` loops — scheduling lives in the platform task.

   ```bash
   #!/bin/bash
   # /home/hatch/keepalive.sh — check muse-console, socat bridge (if §7 applies),
   # and the rathole client. Single run, idempotent, exits immediately.
   set -u
   BASE="/home/hatch/workspace/muse-console"

   # 1) muse-console: healthy if 8080 answers 302 to /login.
   if [ "$(curl -s --max-time 5 -o /dev/null -w '%{http_code}' \
       http://127.0.0.1:8080/)" != "302" ]; then
       pid=$(ss -tlnp 2>/dev/null | grep ":8080 " \
             | grep -oP 'pid=\K[0-9]+' | head -1)
       [ -n "$pid" ] && kill "$pid" 2>/dev/null
       sleep 1
       cd "$BASE" && setsid nohup ./muse-console -config "$BASE/config.toml" \
           > muse-console.log 2>&1 < /dev/null &
   fi

   # 2) socat egress bridge (only if §7 applies).
   # if ! pgrep -f "[s]ocat TCP-LISTEN:12334" >/dev/null 2>&1; then
   #     setsid nohup socat TCP-LISTEN:12334,bind=127.0.0.1,reuseaddr,fork \
   #         PROXY:<proxy-host>:<relay-ip>:<control-port>,proxyport=<proxy-port> \
   #         > socat.log 2>&1 < /dev/null &
   # fi

   # 3) rathole client.
   # if ! pgrep -f "[r]athole -c client[.]toml" >/dev/null 2>&1; then
   #     cd "$RATHOLE_DIR" && setsid nohup ./rathole -c client.toml \
   #         > rathole_client.log 2>&1 < /dev/null &
   # fi
   ```
   Ensure it is executable: `chmod +x /home/hatch/keepalive.sh`.

2. **Create the platform scheduled task:** Schedule a task to run `/home/hatch/keepalive.sh` every 5 minutes, silent on success. Each execution wakes the agent and boots the VM if down.

After creating both, verify with §8's checklist.

## 10. Troubleshooting

- **Public port connects but returns empty / EOF**: the control channel is up but the server has no matching service — wrong service name/token on the server side, or the server rathole didn't reload its config. Have the user re-check §6 server snippet and restart the server.
- **Port already in use**: another instance is running; find it by listening port (`ss -tlnp`) and stop it before starting a new one — never run two writers on the same `timeline.jsonl`.

## 11. Security notes for the agent

- `config.toml` holds **plaintext passwords**: keep it `chmod 600`; never paste passwords or tokens into logs, docs, issues, or long-term memory; never commit it to git.
- The `muse-console` service exposes a **terminal and a file browser** — it must stay behind the login wall. Never map it to a public port without auth, and never disable the login wall to "simplify" access.
- Temporary passwords are single-use: generate a fresh one for every (re)setup, and never reuse one across deployments.
- Brute-force protection is built in: 10 failed password attempts per 10 minutes from one IP → 30-minute block (HTTP 429), covering both the login form and HTTP Basic Auth; a successful login clears the record. Behind rathole all clients share the tunnel endpoint's IP, so the limit is effectively global — tell the user this trade-off (an active attack locks out legitimate users too, until the block expires). Optional `trusted_proxy_headers` (`X-Forwarded-For` / `X-Real-IP`) enables per-client IPs, but only behind a trusted reverse proxy; disabled by default (see `config.example.toml`).
