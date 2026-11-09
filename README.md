# 晨曦 Chenxi

> 一台 1 GB 内存的 S905M 电视盒子，也能有一个记得住你、会自己长本事的 AI 管家。

**目标硬件**：Amlogic S905M（GXBB，Cortex-A53 ×4）/ 1 GB RAM / 16 GB eMMC
**目标系统**：Armbian（minimal CLI 镜像）
**语言**：Go（主运行时） + Rust（副作用守护进程），零第三方依赖、零 cgo、零容器

[![CI](https://github.com/rainrainrainbow/chenxi/actions/workflows/ci.yml/badge.svg)](https://github.com/rainrainrainbow/chenxi/actions/workflows/ci.yml)
[![Release](https://github.com/rainrainrainbow/chenxi/actions/workflows/release.yml/badge.svg)](https://github.com/rainrainrainbow/chenxi/actions/workflows/release.yml)
[![Nightly](https://github.com/rainrainrainbow/chenxi/actions/workflows/nightly.yml/badge.svg)](https://github.com/rainrainrainbow/chenxi/actions/workflows/nightly.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)

**仓库**：[rainrainrainbow/chenxi](https://github.com/rainrainrainbow/chenxi) · **产物**：[Actions artifacts](https://github.com/rainrainrainbow/chenxi/actions) / [Releases](https://github.com/rainrainrainbow/chenxi/releases)

---

## 为什么不是直接装那四款

上一轮源码分析里，四款工具在 1 GB RAM 上都跑不动：

| 项目 | 卡在哪 | 晨曦的做法 |
|---|---|---|
| OpenClaw | Node 24 + 65,893 文件 / 744 MB 源码树，常驻内存数百 MB | 只取「Gateway 中心化 + 串行 agent loop + 单一状态源 + 入站不可信」的设计，用 Go 单二进制重写 |
| Hermes Agent | Python 3.14 + 1.4 GB 仓库，TUI/网关/Desktop 并存 | 只取「SOUL/AGENTS 宪法 + 记忆—技能闭环 + cron + 模型无关 provider」，用标准库实现 |
| Operit2 | Flutter/Dart 引擎在 1 GB 上过重，Rust Core 本身很轻 | 完整保留它的「Rust Host 能力边界 + 四层权限 + trait 契约」，砍掉 Flutter 与多端 UI |
| 璇玑 | Electron 40（Chromium）直接爆内存；多 Agent 端到端未验证 | 保留「声明式 Agent 注册 + 热重载 + L0–L2 分层 Prompt + 5 种协作策略」，但限并发 ≤2、用规则分类 |

一句话：**取四者的大脑，砍掉四者的身体**。所有带界面的部分换成 HTTP + CLI，所有本地大模型换成云端 API。

---

## 硬约束（写进代码，不是写在嘴上）

| 资源 | 预算 | 实现位置 |
|---|---|---|
| Go 主进程常驻 | ≤ 64 MiB（软上限 128 MiB） | `main.go` 的 `debug.SetMemoryLimit(128<<20)` + `GOGC=50` |
| Rust hostd 常驻 | ≤ 8 MiB | 无 tokio、无 serde、无异步运行时，纯 `std`；并发执行原子计数硬限 2 |
| 内存索引 | ≤ 20,000 条，超限淘汰最旧 | `internal/cx/memory.go` |
| 单次工具输出 | ≤ 64 KiB | `hostd/src/capability.rs` 截断 |
| 单次会话上下文 | ≤ 12,000 字符，超出压缩成摘要 | `internal/cx/loop.go` |
| 并发 | 全局 ≤ 2，子 Agent 默认 1 | `internal/cx/team.go` |
| 磁盘 | 事件日志 ≤ 256 MiB、快照 ≤ 32 MiB、日志轮转 | `config.json` 的 `budgets` |
| 本地大模型 | **明确禁用** | `internal/cx/provider.go` 返回硬错误 |

1 GB 内存放不下任何可用的量化 LLM（0.5B Q4 + KV cache 就要 400 MB 以上），所以晨曦默认走云端 OpenAI 兼容接口，也可以指向局域网里另一台机器。`local` provider 是留桩，会直接报错并解释原因，绝不假装能跑。

---

## 架构

```
  CLI / HTTP / Webhook              ← 只做输入输出，不持能力
          │
          ▼
  ┌───────────────────────────────────────┐
  │  Go: chenxi（单进程，静态二进制）        │
  │  Gateway · 串行 Agent Loop · L0/L1/L2   │
  │  AgentRegistry(热重载) · 5 协作策略      │
  │  Memory(JSONL+倒排) · Skill · Cron      │
  │  Provider(云模型)                       │
  └──────────────┬────────────────────────┘
                 │ Unix Domain Socket · 扁平 JSON 行
                 ▼
  ┌───────────────────────────────────────┐
  │  Rust: chenxi-hostd                    │
  │  Capability trait · 四层权限 · 审批     │
  │  审计日志 · 输出截断 · 超时             │
  └───────────────────────────────────────┘
                 │
         文件 / 终端 / 网络（唯一出口）
```

**关键设计**：Go 侧**不 import `os/exec`、不直接写业务文件**。所有副作用必须经过 UDS 交给 hostd，权限边界天然就是进程边界。这来自 Operit2 的 Host 契约，也顺带避开了 cgo 交叉编译的地狱。

---

## 快速开始

### 1. 拿到 arm64 二进制（三选一）

**推荐：直接用 CI 产物**，本地不用装任何工具链。Actions 在 GitHub 原生
`ubuntu-24.04-arm` runner 上编译，去 [Actions artifacts](https://github.com/rainrainrainbow/chenxi/actions)
或 [Releases](https://github.com/rainrainrainbow/chenxi/releases) 下载即可。

在 **aarch64 主机**（Armbian 板子 / arm64 runner）上原生编译，最省事：

```bash
make build-local
# 产物：dist/chenxi（Go 静态）、dist/chenxi-hostd（Rust，动态链接系统 glibc）
```

在 x86 开发机上交叉编译（需 `gcc-aarch64-linux-gnu` + `libc6-dev-arm64-cross`
与 Rust 的 `aarch64-unknown-linux-gnu` target）：

```bash
make build-arm64
```

> 交叉链接对发行版 sysroot 很敏感（Ubuntu 24.04 上常见 `-lgcc_s` 找不到）。
> 失败时别硬调，走前两条路径。Go 侧纯标准库，`CGO_ENABLED=0 GOOS=linux
> GOARCH=arm64 go build` 即可；Rust 侧零 crate，在目标架构上 `cargo build --release` 即可。

### 2. 部署到 Armbian

```bash
# 开发机：把二进制与配置一起送过去
scp dist/chenxi dist/chenxi-hostd chenxi@box:~/
scp config.json agents.json SOUL.md AGENTS.md deploy/hostd.json chenxi@box:~/
scp deploy/*.service chenxi@box:~/

ssh chenxi@box
sudo install -m 0755 chenxi chenxi-hostd /usr/local/bin/
sudo groupadd -f chenxi
sudo useradd -r -s /usr/sbin/nologin -g chenxi chenxi 2>/dev/null || true
sudo useradd -r -s /usr/sbin/nologin -g chenxi chenxi-host 2>/dev/null || true
sudo mkdir -p /etc/chenxi /var/lib/chenxi/workspace /var/log/chenxi
sudo install -m 0644 config.json agents.json SOUL.md AGENTS.md hostd.json /etc/chenxi/
sudo install -m 0644 chenxi.service chenxi-hostd.service /etc/systemd/system/
```

> `SOUL.md` / `AGENTS.md` / `agents.json` 用相对路径读取，必须与 systemd 的
> `WorkingDirectory=/etc/chenxi` 对齐；两个服务不同 UID 但同属 `chenxi` 组，
> 这样 `chenxi` 才能连上 hostd 的 `0660` socket。

配好模型（API Key 走环境变量，不写进配置文件）：

```bash
sudo sed -i 's#https://api.openai.com/v1#https://your-endpoint/v1#' /etc/chenxi/config.json
echo 'CHENXI_API_KEY=sk-xxx' | sudo tee /etc/chenxi.env
sudo chmod 600 /etc/chenxi.env
sudo systemctl daemon-reload
sudo systemctl enable --now chenxi-hostd chenxi
```

### 3. 用起来

```bash
curl -s localhost:7788/v1/health
curl -s localhost:7788/v1/message -d '{"text":"帮我看看 /etc 下有多少配置文件"}'
curl -s localhost:7788/v1/status          # 内存、事件数、技能数、Agent 数
```

或直接进交互 CLI：`sudo -u chenxi /usr/local/bin/chenxi -cli -config /etc/chenxi/config.json`。

硬件层面的 Armbian 调校（设备树、zram、禁服务、eMMC 保护）见 [`deploy/armbian-tuning.md`](deploy/armbian-tuning.md)。

---

## DevOps

不用在本地装交叉工具链——push 上去，Actions 直接产出可 `scp` 到板子的 arm64 二进制。

| Workflow | 触发 | 做什么 |
|---|---|---|
| `ci.yml` | push / PR / 手动 | 架构护栏 → `go vet` + `go test` → `cargo check` + `cargo test` → 原生 `ubuntu-24.04-arm` runner 上 `make build-local` → 校验架构/体积/链接方式 → 上传 artifact（保留 14 天） |
| `release.yml` | `v*` tag / 手动 | 同样在 arm64 runner 上构建，打包 `tar.gz` + `SHA256SUMS`，自动创建 GitHub Release |
| `nightly.yml` | 每周一 / 手动 | `go test -race`、`govulncheck`、`cargo test --release`，外加独立 arm64 job 做编译复核 |

`scripts/check-guardrails.sh` 把「零第三方依赖 / 无 `os/exec` / 无 cgo / 禁用本地推理 / hostd 四层权限」变成 CI 里**会红的断言**；`scripts/verify-artifacts.sh` 检查产物确为 AArch64、体积未越预算、Go 产物为静态链接。dependabot 每周检查 Actions 版本（以及两套空依赖清单）。

从 Release 取产物：

```bash
curl -LO https://github.com/rainrainrainbow/chenxi/releases/latest/download/chenxi-linux-arm64-v0.1.0.tar.gz
sha256sum -c SHA256SUMS
tar -xzf chenxi-linux-arm64-v0.1.0.tar.gz
cd chenxi && cat README.md   # 按「部署到 Armbian」继续
```

> 两个二进制的链接方式不同：`chenxi` 是纯 Go 静态链接；`chenxi-hostd` 由
> `aarch64-linux-gnu-gcc` 链接，动态依赖目标系统自带的 glibc（Armbian 上有）。

---

## 目录

| 路径 | 作用 |
|---|---|
| `SOUL.md` / `AGENTS.md` | Hermes 式可机读人格宪法与运行宪法，注入 L0 |
| `config.json` | 全部运行参数与资源预算（改完重启生效） |
| `agents.json` | 璇玑式声明式 Agent，**改完自动热重载** |
| `cmd/chenxi/main.go` | 装配、内存上限、信号退出 |
| `internal/cx/*.go` | Gateway / Loop / Prompt / Agents / Team / Memory / Skill / Provider / Cron / HostClient |
| `hostd/src/*.rs` | Operit2 式 Capability trait、四层权限、审计 |
| `deploy/` | systemd unit 与 Armbian 调校指南 |
| `scripts/` | CI 护栏与产物校验脚本 |
| `.github/workflows/` | CI / Release / Nightly 三条流水线 |
| `SECURITY.md` / `CONTRIBUTING.md` | 信任模型与贡献规矩 |
| `DESIGN.md` | 为什么这么切、协议细节、不做什么 |

---

## 明确不做的事

- ❌ 本地 LLM、Embedding、向量库（1 GB 放不下）
- ❌ Electron / Flutter / Web UI / TUI 富终端（Chromium 与 Dart 引擎太重）
- ❌ Node / Python 运行时、Docker、cgo、SQLite 动态链接
- ❌ 多租户隔离：`SECURITY` 模型沿用 OpenClaw 的「单可信操作者」，别把 7788 端口直接开公网
- ❌ 多设备 Space 同步：只保留 Operit2 的**权限边界**，不实现 Link/Peer 组网（1 GB 下收益不抵复杂度）

## 许可

MIT。
