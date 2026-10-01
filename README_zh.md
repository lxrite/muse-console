# muse-console

[English](README.md) | 简体中文

> **TL;DR**：Meta 最近推出的 **Muse from Meta** 会给每个用户分配一台云端 Linux 虚拟机跑 Agent。这台机器没有公网 IP，且 Agent 干完活后随时可能被销毁（仅保留特定持久化目录）。  
> 但只要搭配 **rathole 隧道穿透** + **定时唤起 Agent 保活**，就能把它变成一台 24 小时在线的免费“云端 VPS”。  
> **本项目就是专为这种场景打造的演示性轻量 Web 控制台。**

---

### 这是个什么项目？

Meta 最近发布的 **Muse from Meta** 让大家体验到了云端 Linux 沙盒的便利 —— 每个用户都能拥有一台属于自己的 Linux 虚拟机，让 AI Agent 在里面跑命令、做任务。

不过默认情况下，这台机器有两个明显的限制：
1. **没有公网 IP**：外部网络无法直接访问它，想连进去只能靠 App 里的聊天框；
2. **生命周期短暂**：Agent 干完活或空闲一段时间后，虚拟机随时会被回收销毁，除了特定目录（如 `/home/hatch`）下的文件，其他改动都会丢失。

**但我们可以通过一些巧妙的配置把它玩成真正的 VPS**：
- **公网穿透**：通过轻量级的 [rathole](https://github.com/rapiz1/rathole) 隧道，把机器内部的端口映射到外部公网中继机上；
- **定时保活**：利用平台支持的定时任务周期性唤起 Agent，机器一旦被杀就会被自动拉起，并在几分钟内重新联网上线；
- 只要把数据和程序放在持久化目录里，这台原本“用完即扔”的临时沙盒，实际上就变成了一台能长期稳定运行的 Linux VPS。

**`muse-console` 就是为了方便在这个场景下管理和观察这台机器而写的 Web 控制台 demo。**

它是一个零运行时依赖的单一 Go 可执行文件（前端页面与终端资源全部内嵌），专门针对临时沙盒设计：
- **系统状态与在线监测**：随时查看 CPU、内存、磁盘占用，并通过 72 小时时间线直观掌握机器是否在线、运行是否稳定；
- **Web 终端**：无需折腾 SSH 服务，在网页端随时打开带完整交互的 Shell；
- **文件管理**：直接通过网页浏览、上传、下载和预览持久化目录中的文件。

```
muse-console
├── /            状态仪表盘（时间、CPU/内存/磁盘、72小时心跳时间线）
├── /term        Web 终端（xterm.js + WebSocket + PTY）
├── /files       文件浏览器（列表 / 下载 / 预览 / 上传 / 新建目录 / 重命名 / 删除）
├── /change-password  修改自己的密码（使当前所有会话失效）
└── /logout      退出登录（清除会话）
```

## 让 Agent 帮你一键部署（推荐）

> **前提条件**：由于 Muse 沙盒机没有公网 IP，你需要自备一台拥有独立公网 IP 的服务器（如云主机/VPS）作为中继机并运行 [rathole](https://github.com/rapiz1/rathole) 服务端。

最省事的方式：**直接把本项目的 GitHub 仓库链接发给 Muse 的 Agent**。

仓库里已经内置了一份专给 Agent 阅读的部署指南 [docs/AGENT_DEPLOY.md](docs/AGENT_DEPLOY.md)。Agent 读取后，会在聊天中以对话的方式向你询问必要的中继信息（并向你解释为什么需要这些信息），然后自动在你的虚拟机里完成下载、配置、rathole 隧道打通和保活设置，全程无需手动敲命令。

## 手动快速上手

```bash
go build -o muse-console .

# 1. 创建配置文件（TOML），并至少包含一个用户
cat > config.toml <<'EOF'
addr = "127.0.0.1:8080"
files_root = "/"

[[users]]
username = "admin"
password = "change-me"
EOF

# 2. 在配置文件中设置你的密码并运行，然后打开
#    http://127.0.0.1:8080/ 进行登录
./muse-console
```

若指定了命令行参数（`-config`、`-addr`、`-data-dir`、`-disk-path`、`-files-root`），则会覆盖配置文件或默认设置。

### 密码管理

`config.toml` 中通过 `[[users]]` 保存明文形式的 `password`（文件权限必须保持为 `chmod 600`）。当由 Agent（重新）创建配置时：请先向用户询问所需的用户名和密码；如果用户未提供，请使用用户名 `admin` 和随机 6 位临时密码，并在对话中告知用户账号及密码。使用临时密码时，用户可在首次登录后通过导航栏中的**修改密码**页面（`/change-password`）自行设置新密码。
修改密码仅会重写 `config.toml` 中该用户的 `password = "..."` 一行（注释与格式均会完整保留），同时会使所有已有会话失效（因为会话签名密钥由用户密码派生而来）。

### 防暴力破解保护

密码登录失败会按客户端 IP 进行频率限制：单个 IP 在 10 分钟内失败 10 次将被封禁 30 分钟（返回 HTTP 429；登录成功会清除失败记录）。该机制同时适用于 Web 登录表单和 HTTP Basic Auth。

由于 muse-console 通常通过 rathole TCP 隧道暴露至公网，所有外部客户端在控制台看来均来自隧道端点 IP —— 因此该限制实际上是**全局**生效的。这是有意为之的权衡设计：能在遭受主动撞库攻击时阻止暴力破解，代价是在攻击期间可能暂时阻断正常用户的登录尝试。

如果 muse-console 运行在设置了 `X-Forwarded-For` / `X-Real-IP` 的**受信任**反向代理之后，你可以通过 `trusted_proxy_headers` 启用基于客户端独立 IP 的限流（参考 `config.example.toml`）。该功能**默认禁用** —— 若在不受信任的代理后启用它，任何攻击者都能伪造该请求头绕过防爆破限制。

## 配置项

`config.toml`（TOML 格式 —— 与 rathole 使用相同格式）：

| 配置项 | 默认值 | 描述 |
|---|---|---|
| `addr` | `127.0.0.1:8080` | 监听地址（登录保护墙：仪表盘 + 终端 + 文件浏览器） |
| `data_dir` | `.` | 保存 `timeline.jsonl` 的数据目录 |
| `disk_path` | `/home/hatch` | 在仪表盘上显示其文件系统磁盘用量的路径 |
| `files_root` | `/` | 文件浏览器被限制访问的根目录（Jail 范围） |
| `trusted_proxy_headers` | `[]` | 用于客户端真实 IP 识别的受信任请求头列表（如 `["X-Forwarded-For"]`） |
| `[[users]]` | _(必填)_ | 一个或多个用户：`username` + 明文 `password` |

## 国际化 (i18n)

UI 支持简体中文和英文，每次请求根据客户端的 `Accept-Language` 请求头自适应匹配（包含任何 `zh` 变体 → 中文，其他情况 → 英文）。所有文案均位于 `i18n.go`；文件浏览器的前端文案通过 `window.I18N` 注入。

## 认证机制

基于用户名 + 密码，以明文方式存储在配置文件中（请务必保持权限为 `chmod 600`，就像对待 SSH 私钥或 rathole token 一样 —— 能读取该文件的人已经能够完全冒充你的身份，使用密码哈希并不会显著增加安全性）。

- 浏览器：在 `/login` 登录成功后会签发一个带 HMAC 签名、标记为 `HttpOnly` 的会话 Cookie（有效期 30 天，剩余有效期少于一半时自动顺延滑动刷新）。`/logout` 会清除该 Cookie。
- API / 脚本：可直接使用 HTTP Basic Auth（如 `curl -u user:pass …`）。
- 未认证的页面访问将重定向至 `/login?next=…`；未认证的 API 与 WebSocket 请求直接返回 `401`。

会话 Cookie 是无状态的：HMAC 密钥由用户的密码哈希派生而成，因此在服务重启后会话依然有效，但修改密码后所有已有会话立即失效。

## 心跳时间线

后台 Goroutine 每分钟记录一次心跳（Unix 时间戳以及 `boot_id`/`uptime`），并写入 `timeline.jsonl` —— 每行一个 JSON 对象。
存储时始终使用 Unix 时间戳（UTC）。页面渲染时会按访问者本地时区显示：服务器在页面中嵌入 UTC 时间戳，前端内联脚本在首次渲染前完成时区转换（无任何闪烁；禁用 JS 时则回退显示为 UTC）。图表坐标轴会自动将 6 小时刻度对齐到本地日期边界。
状态分类采用二元判定：

- **正常运行 (active)**（绿色）— 心跳间隔 ≤ 90 秒：视为正常的时间抖动。
- **离线 (offline)**（红色）— 心跳间隔 > 90 秒：服务未正常记录心跳。每个离线事件都会标明 `boot_id` 是否发生变化，以区分是机器本身发生重启（“离线（机器重启）”），还是单纯的心跳中断（“离线”）。
- **无数据 (unknown)**（灰色）— 完全无数据（首条记录产生之前，或日志文件为空）。

## 通过 rathole 暴露到公网

本程序在设计上仅监听本地地址。若需从公网访问，可在本地运行 [rathole](https://github.com/rapiz1/rathole) 客户端连接到它：

```toml
# rathole client.toml
[client]
remote_addr = "your-relay:12333"

[client.services.muse-console]
type = "tcp"
local_addr = "127.0.0.1:8080"
```

如果运行环境只能通过 HTTP CONNECT 代理访问外网，可以使用 socat 进行桥接转发（rathole 本身不支持代理协议）：

```bash
socat TCP-LISTEN:12334,bind=127.0.0.1,reuseaddr,fork \
    PROXY:<proxy-host>:<relay-host>:12333,proxyport=3128
# 之后将 client.toml 中的 remote_addr 配置为 "127.0.0.1:12334"
```

## 安全注意事项

- 登录后将获得**根级别 Shell 权限**（即二进制运行所使用的系统用户）。请选择强密码，并确保 `config.toml` 仅对当前用户可读（`chmod 600`）。
- 密码以明文形式保存在 `config.toml` 中 —— 请严格保持 `chmod 600` 权限，切勿使用在其他重要场合用过的密码。如需轮换密码，可直接编辑文件或在网页端修改；密码变更后所有旧会话将全部失效。
- 文件浏览器受限于 `files_root`（拒绝任何 `..` 路径逃逸），但由于登录用户本身已具备执行任意命令的能力，因此该路径限制属于纵深防御措施，而非绝对的安全边界。
- 建议仅绑定到 `127.0.0.1` 并在前方配置反向代理或隧道；除非你完全清楚其安全风险，否则不要直接将监听端口暴露到公网。

## 项目结构

```
.
├── docs/               Agent 部署指南 (AGENT_DEPLOY.md)
├── static/             xterm.js 静态资源（内嵌）
├── templates/          change_password.html, files.html, login.html, status.html, term.html（内嵌）
├── LICENSE             MIT 许可证文件
├── auth.go             基于 HMAC 的会话 Cookie、/login、/logout、防爆破限流与认证中间件
├── auth_test.go        会话签发/校验/防篡改/过期测试、登录流程及防爆破限流单元测试
├── config.go           TOML 配置加载与合法性校验
├── config.example.toml 配置文件示例
├── files.go            文件浏览器 JSON API（限制在 files_root 目录内）
├── i18n.go             UI 多语言翻译字典（中/英）与 Accept-Language 协商
├── i18n_test.go        i18n 翻译字典与语言协商单元测试
├── main.go             程序主入口：配置加载、命令行参数、路由装配与服务启动
├── status.go           系统指标采集、心跳记录器、离线间隔判定与 SVG 图表生成
├── status_test.go      时间线分类与路径沙箱单元测试
├── templates.go        内嵌模板解析与渲染辅助方法
└── terminal.go         WebSocket <-> PTY 终端桥接
```

使用 `go:embed` 将 `static/` 与 `templates/` 打包至二进制文件中：构建产物为单个自包含可执行文件，运行时无需任何外部资源。

## 许可证

本项目基于 [MIT](LICENSE) 许可证开源。
