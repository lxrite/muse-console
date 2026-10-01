# muse-console

English | [简体中文](README_zh.md)

> **TL;DR**: Meta's new **Muse from Meta** app gives each user a cloud Linux sandbox VM to run AI agents. The VM lacks a public IP and gets destroyed shortly after the agent finishes (only a persistent directory survives).  
> By pairing a **rathole TCP tunnel** with a **scheduled keepalive task**, you can keep the VM running indefinitely — essentially turning it into a free, persistent cloud VPS.  
> **`muse-console` is a proof-of-concept web console tailored specifically for this setup.**

---

### What is this?

Meta recently released **Muse from Meta**, providing users with an ephemeral cloud Linux VM where AI agents can execute tasks and run commands.

Out of the box, these VMs have two major constraints:
1. **No public IP**: Hidden behind NAT and firewalls, there is no direct way to access the machine from the outside world.
2. **Short lifespan**: When the agent finishes or goes idle, the VM is destroyed. Only files in designated persistent storage (such as `/home/hatch`) survive.

**However, there is a way to turn it into an actual VPS:**
- **Tunneling**: Route local ports out to the internet through an encrypted [rathole](https://github.com/rapiz1/rathole) TCP tunnel to a relay VPS.
- **Keepalive**: Use the platform's scheduled tasks to periodically wake up the agent (e.g., every 5 minutes). Even if the VM gets recycled, the scheduled task revives it within minutes.
- Combined with persistent storage, the ephemeral sandbox behaves like a permanent, always-on Linux server.

**`muse-console` was built as a lightweight, demo web console specifically for managing and monitoring these sandboxes.**

It ships as a single Go binary with all assets embedded:
- **Status & Online Monitoring**: Check CPU, memory, and disk usage at a glance, with a 72-hour timeline to see whether the VM is online and running stably.
- **Web Terminal**: Instant interactive shell in the browser via PTY + WebSocket, without needing to configure SSH keys or open SSH ports.
- **File Browser**: Browse, upload, download, and preview files stored in the persistent directory.

```
muse-console
├── /            status dashboard (time, CPU/mem/disk, 72h heartbeat timeline)
├── /term        web terminal (xterm.js + WebSocket + PTY)
├── /files       file browser (list / download / preview / upload / mkdir / rename / delete)
├── /change-password  change own password (invalidates sessions)
└── /logout      clear session
```

## Deploy with Muse Agent (Recommended)

> **Prerequisite**: Because the Muse sandbox VM has no public IP, you must have a server/VPS with a public IP to act as the [rathole](https://github.com/rapiz1/rathole) relay.

The easiest way to get started: **just paste this GitHub repository link to your Muse agent**.

This repository comes with [docs/AGENT_DEPLOY.md](docs/AGENT_DEPLOY.md), a step-by-step deployment guide written specifically for AI agents. The agent will converse with you in chat, ask for any needed relay information (explaining *why* along the way), and automatically configure, launch, tunnel, and set up keepalive in your VM without manual intervention.

## Manual quick start

```bash
go build -o muse-console .

# 1. create a config file (TOML) with at least one user
cat > config.toml <<'EOF'
addr = "127.0.0.1:8080"
files_root = "/"

[[users]]
username = "admin"
password = "change-me"
EOF

# 2. set your password in the config and run it, then open
#    http://127.0.0.1:8080/ and log in
./muse-console
```

Command-line flags (`-config`, `-addr`, `-data-dir`, `-disk-path`,
`-files-root`) override the config file or default settings when given.

### Passwords

`config.toml` holds `[[users]]` with a plaintext `password` (file must stay
`chmod 600`). When the config is (re)created by an agent: ask the user for
the username and password they want; if they don't provide them, use
username `admin` with a random 6-digit temporary password, and tell the user
the account + password in chat. With a temporary password, the user sets
their own after the first login via the **Change password** page (`/change-password`,
linked from the dashboard nav).
Changing the password rewrites only that user's `password = "..."` line in
`config.toml` (comments and formatting are preserved) and invalidates all
existing sessions, since the session signing key is derived from the
password.

### Brute-force protection

Failed password attempts are rate-limited per client IP: 10 failures within
10 minutes block the IP for 30 minutes (HTTP 429; a successful login clears
the record). This covers both the login form and HTTP Basic Auth.

Because muse-console is usually exposed through a rathole TCP tunnel, all
external clients appear to come from the tunnel endpoint's IP — the limit is
therefore effectively **global**. This is intentional: it stops brute-forcing
at the cost of temporarily locking out legitimate users during an active
attack.

If muse-console sits behind a **trusted** reverse proxy that sets
`X-Forwarded-For` / `X-Real-IP`, you can opt into per-client IPs with
`trusted_proxy_headers` (see `config.example.toml`). It is **disabled by
default** — enabling it with an untrusted proxy lets anyone spoof the header
and bypass the limit.

## Config

`config.toml` (TOML — same format rathole uses):

| Key                     | Default          | Description                                                    |
|-------------------------|------------------|----------------------------------------------------------------|
| `addr`                  | `127.0.0.1:8080` | listen address (login wall: dashboard+term+files)              |
| `data_dir`              | `.`              | directory holding `timeline.jsonl`                             |
| `disk_path`             | `/home/hatch`    | path whose filesystem disk stats are shown                     |
| `files_root`            | `/`              | root directory the file browser is jailed to                   |
| `trusted_proxy_headers` | `[]`             | request headers trusted for client-IP detection (e.g. `X-Forwarded-For`) |
| `[[users]]`             | _(required)_     | one or more users: `username` + plaintext `password`           |

## i18n

The UI is available in Simplified Chinese and English, picked per request
from the `Accept-Language` header (any `zh` variant → Chinese, anything else
→ English). All strings live in `i18n.go`; the file browser's client-side
strings are injected as `window.I18N`.

## Auth

Username + password, stored in plaintext in the config file (keep it
`chmod 600`, like an SSH key or rathole token — anyone who can read the
file can already impersonate you, so a hash would add little).

- Browser: a successful login at `/login` issues an HMAC-signed,
  `HttpOnly` session cookie (30 days, sliding refresh past half-life).
  `/logout` clears it.
- API / scripts: use HTTP Basic Auth (`curl -u user:pass …`).
- Unauthenticated page visits redirect to `/login?next=…`; unauthenticated
  API and WebSocket requests get `401`.

Session cookies are stateless: the HMAC key is derived from the user's
password hash, so sessions survive restarts but die on a password change.

## Heartbeat timeline

A background goroutine records a heartbeat every minute (unix time plus
`boot_id`/`uptime`) into `timeline.jsonl` — one JSON object per line.
Storage is always unix timestamps (UTC). Displayed times are rendered in
the viewer's local timezone: the server embeds UTC timestamps in the page
and a small script converts them before first paint (no flash; without JS
they show as UTC). The chart axis aligns its 6h ticks to local day
boundaries.
Classification is binary:

- **active** (green) — gap ≤ 90s between heartbeats: normal jitter.
- **offline** (red) — gap > 90s: the service wasn't heartbeating. Each
  offline event notes whether the `boot_id` changed, i.e. whether the
  machine itself rebooted ("Offline (rebooted)") or it was just a heartbeat
  interruption ("Offline").
- **unknown** (gray) — no data at all (before the first record, or empty
  journal).

## Exposing it with rathole

The binary only listens locally by design. To reach it from the public
internet, run a [rathole](https://github.com/rapiz1/rathole) client
pointing at it:

```toml
# rathole client.toml
[client]
remote_addr = "your-relay:12333"

[client.services.muse-console]
type = "tcp"
local_addr = "127.0.0.1:8080"
```

If the sandbox can only egress through an HTTP CONNECT proxy, bridge it
with socat (rathole does not speak proxy protocols itself):

```bash
socat TCP-LISTEN:12334,bind=127.0.0.1,reuseaddr,fork \
    PROXY:<proxy-host>:<relay-host>:12333,proxyport=3128
# then remote_addr = "127.0.0.1:12334"
```

## Security notes

- A login grants a **root shell** (whatever user the binary runs as).
  Pick a strong password and keep `config.toml` readable only by you
  (`chmod 600`).
- Passwords are stored in plaintext in `config.toml` — keep it `chmod 600`
  and don't reuse a password that matters elsewhere. Rotate by editing the
  file and restarting; all existing sessions are invalidated by the change.
- The file browser is jailed to `files_root` (`..` escapes are rejected),
  but a logged-in user can already run arbitrary commands — the jail is
  defense in depth, not a security boundary.
- Bind to `127.0.0.1` and put a reverse proxy / tunnel in front; do not
  expose the port directly unless you know what you are doing.

## Layout

```
.
├── docs/               agent deployment guide (AGENT_DEPLOY.md)
├── static/             xterm.js assets (embedded)
├── templates/          change_password.html, files.html, login.html, status.html, term.html (embedded)
├── LICENSE             MIT License
├── auth.go             session cookies (HMAC), /login, /logout, rate limiting, auth middleware
├── auth_test.go        session issue/verify/tamper/expiry, login flow, brute-force rate limiter
├── config.go           TOML config loading and validation
├── config.example.toml example configuration file
├── files.go            file browser JSON API (jailed to files_root)
├── i18n.go             UI translation dictionaries (EN/ZH) and Accept-Language negotiation
├── i18n_test.go        unit tests for i18n translation and language negotiation
├── main.go             wiring: config, flags, routes, startup
├── status.go           system stats, heartbeat recorder, gap classification, SVG chart
├── status_test.go      unit tests for timeline classification and path jail
├── templates.go        embedded template loading and rendering helper
└── terminal.go         WebSocket <-> PTY bridge
```

`go:embed` bakes `static/` and `templates/` into the binary:
the build artifact is one self-contained executable, no runtime files needed.

## License

This project is licensed under the [MIT License](LICENSE).
