# ⚒️ ForgeC2

**一个二进制装下的自托管命令与控制平台。** <!--fact:tasks-->222<!--/fact--> 个植入体任务、9 种传输、浏览器内的载荷工厂与 AI 副驾——发布产物签名可验证，热更新失败自动回滚。

[![CI](https://github.com/r0si/forgec2/actions/workflows/ci.yml/badge.svg)](https://github.com/r0si/forgec2/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/r0si/forgec2)](https://github.com/r0si/forgec2/releases)
[![Downloads](https://img.shields.io/github/downloads/r0si/forgec2/total)](https://github.com/r0si/forgec2/releases)
[![Stars](https://img.shields.io/github/stars/r0si/forgec2)](https://github.com/r0si/forgec2/stargazers)
[![Contributors](https://img.shields.io/github/contributors/r0si/forgec2)](https://github.com/r0si/forgec2/graphs/contributors)
[![Last commit](https://img.shields.io/github/last-commit/r0si/forgec2)](https://github.com/r0si/forgec2/commits/main)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Go 1.26](https://img.shields.io/badge/go-1.26-00ADD8?logo=go&logoColor=white)](go.mod)

[English](README.md) · **中文** · [快速开始](#快速开始) · [截图](#截图) · [文档](#文档)

> ⚠️ **仅限授权安全测试。** 对任何系统使用前，必须持有目标所有者的明确书面许可。见[法律声明](#法律声明)。

![ForgeC2 作战仪表盘](docs/assets/screens/dashboard.png)

---

## 快速开始

### 方式 A —— 一键归档（无需工具链）

| 平台 | 步骤 |
|---|---|
| **Windows** | 解压 `forgec2-v2.7.1-windows-amd64.zip`，进入目录运行 `start.cmd` |
| **Linux** | `tar xzf forgec2-v2.7.1-linux-amd64.tar.gz && cd forgec2-v2.7.1-linux-amd64 && ./start.sh` |

首次启动会：

1. 用 `config.example.yaml` 生成 `config.yaml`；
2. 在控制台打印随机生成的 `admin` 密码；
3. 在 **<https://127.0.0.1:8000/>** 提供控制台（自签名证书）。

浏览器点一次“继续访问”，用 `admin` 登录后立即修改密码。

### 方式 B —— Docker

```bash
cp config.example.yaml config.yaml     # 必需：compose 直接挂载这个文件
docker compose up -d --build
```

同样在 `https://127.0.0.1:8000/` 打开，数据落在 `forgec2_data` 卷里。

### 方式 C —— 源码构建

需要 Go <!--fact:go-->1.26<!--/fact-->+ 与 Node.js >= <!--fact:node-->24.15.0<!--/fact-->：

```bash
git clone https://github.com/r0si/forgec2.git && cd forgec2

# Windows
powershell -File scripts/deploy.ps1 -NoStart

# Linux / macOS（没有 pwsh 时自动回退到 npm + go build）
bash scripts/deploy.sh --no-start
```

`deploy.*` 会构建前端、同步进 `internal/webdist/dist`、跑前端门禁并产出
`forgec2-server`。去掉 `-NoStart` / `--no-start` 就会顺带重启实例，并做
`/health` 探测与失败回滚。

> **要从别的机器访问？** 控制台默认监听 `0.0.0.0`。把那台机器的主机名加进
> `server.allowed_origins`，否则所有 API 调用都会被当作跨域拒绝。详见
> [docs/DEPLOY.md](docs/DEPLOY.md)。

---

## 里面有什么

| | |
|---|---|
| 🚀 **一个文件就是全部** | React 控制台、REST API、信标端点与 SQLite 全部跑在一个进程、一个端口里。没有前端服务要守，没有数据库引擎要哄，没有运行时依赖要养。 |
| 🧬 **载荷工厂** | EXE / DLL / PowerShell / ELF / macOS 植入体、XOR stager、shellcode、Donut、一句话，全部服务端交叉编译。自定义图标与 JPG 双扩展名伪装内置。 |
| 📡 **九种传输 + P2P / 外部 C2** | HTTP(S)、WSS、gRPC、mTLS、H2C、TCP、DNS、ICMP、SSH，外加 SMB/TCP P2P 级联、Discord/Slack 外部 C2，以及实验性的 QUIC/UDP。 |
| 🤖 **AI 副驾** | DeepSeek、OpenAI、Claude 或任何 OpenAI 兼容模型。在聊天里直接指挥整场行动，支持工具调用与预算感知的上下文。 |
| 🛡️ **OPSEC 是功能** | 行动前规则引擎、可塑流量画像（malleable）、AMSI/ETW 对抗、睡眠掩码，整套载荷管线都带防呆护栏。 |
| 🧩 **为扩展而生** | <!--fact:plugins-->52<!--/fact--> 个即插插件、JavaScript 脚本引擎、工作流自动化，以及完整的 OpenAPI 面。 |

## 植入体能力

<!--fact:tasks-->222<!--/fact--> 条任务规格，统一定义在共享注册表里，团队服务器与植入体共用同一份定义：

- **立足** —— shell、PowerShell、execute-assembly、BOF、PowerPick、PE/CLR 加载、令牌窃取/伪造/还原、凭据、mimikatz、kerberoast、DCSync
- **横向** —— WMI、WinRM、PsExec、Pass-the-Hash、Pass-the-Ticket、SMB/TCP 中继、SOCKS5、端口转发、NTLM relay
- **持久化** —— 注册表、计划任务、启动项、WMI、服务、COM 劫持、IFEO
- **对抗** —— AMSI/ETW 绕过、VEH 脱钩、硬件断点、睡眠掩码、沙箱检测
- **监视** —— 截图、实时屏幕、窗口标题键盘记录、录制、剪贴板、远程输入
- **侦察** —— cookie 导出、VPN/WiFi 凭据、端口扫描、进程树、OS/域发现

完整的分任务、分系统矩阵（含 C 植入体差异）：[docs/CAPABILITY_MATRIX.md](docs/CAPABILITY_MATRIX.md)

## 截图

![ForgeC2 登录页](docs/assets/screens/login.png)

控制台共 <!--fact:routes-->43<!--/fact--> 个路由页面：实时作战仪表盘、载荷生成器、
主机详情（文件、终端、持久化、屏幕、令牌）、令牌实验室、可塑画像、战役、钓鱼、
BloodHound 摄入，以及十五个设置分区——暗色与亮色主题齐备。

## 传输

| 传输 | 状态 | 说明 |
|---|---|---|
| HTTP(S) | Core | 全平台默认通道 |
| TCP | Core | 裸 socket 监听 |
| DNS | Hardened | 支持 DoH/DoT 字段，查询分片 |
| WSS | Hardened | 持久 WebSocket、二进制帧、ping |
| gRPC | Hardened | `grpcs://` TLS + keepalive；配置客户端 CA 后走 mTLS |
| SSH | Experimental | 账号密码与主机密钥固定（host-key pinning） |
| ICMP | Hardened | 信封拆分进 Echo 载荷 |
| UDP | Experimental | 每个信标一个数据报 |
| QUIC | Experimental | TLS 1.3，ALPN `h3`/`fc2`，流式封帧 |
| mTLS / H2C | Experimental | 生成表单里仅作选择项 |
| SMB P2P | Hardened | Windows 命名管道 / Unix socket |

状态口径与 [docs/CAPABILITY_MATRIX.md](docs/CAPABILITY_MATRIX.md) 一致——那份矩阵是唯一事实源，由任务注册表重新生成。

## 供应链与更新

C2 基础设施本身就是高价值目标，所以发布链路的目标是“可核查”，而不是“请相信”：

- **签名发布。** 每个二进制与归档都带 `.sha256` 和独立的 Ed25519 `.sig`。tag 构建缺少签名密钥会**直接失败**（fail closed）。
- **SBOM** 随 Release 一起发布，与逐个产物的校验和放在一起。
- **可验证的热更新。** 服务端下载匹配资产 → 校验 checksum 与签名 → 快照数据库 → 通过生成的 restarter 重启 → 把结果写进 `.update/last`。新版本起不来会回滚到上一个二进制。并发更新用 `409` + 锁文件拒绝。
- **12 个 CI job** 覆盖格式化与 vet、golangci-lint（含 gosec）、govulncheck、gitleaks、race detector、fuzz、C 植入体构建、Docker、API smoke、agent E2E、前端门禁与 live E2E。
- **410 个 Go 测试文件、182 个前端测试文件**，外加 OpenAPI 契约校验、i18n 键覆盖、内嵌前端产物字节级一致校验。

## 与其他框架的对比

| | **ForgeC2** | Sliver | Mythic | Havoc | Cobalt Strike |
|---|---|---|---|---|---|
| **部署形态** | 单二进制，内嵌 Web UI | 服务端 + CLI 客户端 | Docker Compose 多容器 | Teamserver + Qt 客户端 | Java teamserver + 桌面客户端 |
| **Web 控制台** | ✅ 内置 | ❌ | ✅ | ❌（Qt 桌面） | ❌（桌面） |
| **传输** | 内置 9 种 + QUIC/UDP/SMB P2P | mTLS、WireGuard、HTTP(S)、DNS | 由可安装的 C2 profile 决定 | HTTP(S)、SMB、外部 C2 | HTTP(S)、DNS、SMB、TCP |
| **载荷构建** | 服务端交叉编译 | 服务端 Go 编译 | 按 agent 容器构建 | Teamserver | Artifact Kit 附加组件 |
| **AI 副驾** | ✅ | ❌ | ❌ | ❌ | ❌ |
| **更新方式** | 签名、内置、可回滚 | 手动替换 | `mythic-cli` 仅检查 | 手动 | 厂商更新器 |
| **许可** | MIT | GPL-3.0 | BSD-3-Clause | GPL-3.0 | 商业 |

*结构性对比，时间点为 2026 年 10 月，信息取自各项目公开文档。这里只比较打包与交付形态，不比较技战术效果或免杀率。*

## 架构

```
                    ┌───────────────────────────────────────────────────────────────┐
   Operators ──────▶│ ForgeC2 — one binary, one port :8000                          │
                    │                                                               │
                    │    ┌──────────────┐  ┌──────────────┐  ┌───────────────────┐  │
                    │    │    Web UI    │  │   REST API   │  │      Beacon       │  │
                    │    │  React+Vite  │  │   Gin + WS   │  │     endpoints     │  │
                    │    │  (embedded)  │  │     + AI     │  │                   │  │
                    │    └──────────────┘  └──────────────┘  └───────────────────┘  │
                    │                                                               │
                    │ SQLite · plugins · scripting · OPSEC rules                    │
                    │ build queue ──▶ cross-compiled implants                       │
                    └───────────────────────────────┬───────────────────────────────┘
                                                    │
             HTTP(S) · WSS · gRPC · mTLS · H2C · TCP · DNS · ICMP · SSH
                                                    │
                    ┌───────────────────────────────▼───────────────────────────────┐
                    │ Windows / Linux / macOS implants (+ P2P)                      │
                    └───────────────────────────────────────────────────────────────┘
```

深挖：[ARCHITECTURE.md](ARCHITECTURE.md)

## 配置

全部收敛在一个 YAML 里——以 [config.example.yaml](config.example.yaml) 为准：

| 键 | 用途 |
|---|---|
| `server.port` / `server.tls_enabled` | 监听地址与 TLS 终结（默认开启，首次启动自动生成自签名证书） |
| `server.allowed_origins` / `cookie_domain` | 跨主机与跨域部署 |
| `implant.default_interval` / `default_jitter` | 信标节拍默认值 |
| `ai.provider` / `api_key` / `model` | AI 助手后端 |
| `rate_limit.login.*` | 登录爆破防护 |

各类密钥（JWT、beacon key、loot/ext-c2/backup/credential/CSRF key）都在首次启动时
生成，并以 `0600` 权限写回同一个文件。

## 文档

- [docs/DEPLOY.md](docs/DEPLOY.md) —— 部署、TLS、反向代理、升级
- [docs/CAPABILITY_MATRIX.md](docs/CAPABILITY_MATRIX.md) —— 分任务、分系统能力矩阵
- [docs/COMMAND_REFERENCE.md](docs/COMMAND_REFERENCE.md) —— 任务与命令参考
- [ARCHITECTURE.md](ARCHITECTURE.md) —— 请求路径、存储、构建管线
- [docs/PLUGIN_AUTHORING.md](docs/PLUGIN_AUTHORING.md) —— 插件清单、分级与签名
- [CONTRIBUTING.md](CONTRIBUTING.md) —— 构建、测试与发版规范
- [SECURITY.md](SECURITY.md) —— 漏洞披露
- [docs/AUDIT_INTEGRITY.md](docs/AUDIT_INTEGRITY.md) —— 审计哈希链：能证明什么，以及校验/导出/锚定的操作流程
- [CHANGELOG.md](CHANGELOG.md) —— 发布历史

## 法律声明

ForgeC2 仅用于**授权安全测试**。对任何系统使用前，必须持有所有者的明确书面许可。见 [LICENSE](LICENSE)。

---

*Forge your access. Control your narrative.（铸访问，控叙事。）*
