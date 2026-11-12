# 晨曦设计说明

## 一、取舍映射：四款工具各拿走什么

| 来源 | 借鉴的设计 | 落地位置 | 为 1 GB RAM 做的裁剪 |
|---|---|---|---|
| **OpenClaw** | Gateway 作为唯一控制面；会话串行化；单一状态源、历史只 append；入站消息一律不可信；plugin 边界与「small core」 | `gateway.go`、`loop.go`、`memory.go` | 砍掉 20+ 渠道适配与 Plugin SDK，只留 CLI + HTTP + webhook 三个入口；用 append-only JSONL 替代 SQLite/Postgres 双引擎与 39/41 版 schema 迁移 |
| **Hermes Agent** | `SOUL.md` / `AGENTS.md` 可机读宪法；记忆 + 技能自进化闭环；cron 定时投递；模型无关 provider；依赖精确锁版本的供应链纪律 | `SOUL.md`、`AGENTS.md`、`skill.go`、`cron.go`、`provider.go`、`go.mod` | 用标准库 bigram 倒排替代 FTS5 + Honcho；`go.mod` 里 **零 require**；本地模型明确禁用；技能提炼改为规则式（不额外调 LLM） |
| **Operit2** | Rust `HostCapability` trait 边界；四层权限（沙箱 → Host 授权 → AI 能力档位 → 用户审批）；子模块只通过 trait 向父级请求能力；审计 | `hostd/src/capability.rs` | 砍掉 Flutter、Wasm、Compose DSL、ToolPkg、MCP、Space/Link/Peer 组网与 Edge Node；只保留一个 `chenxi-hostd` 进程 |
| **璇玑** | 声明式 Agent 注册 + 哈希热重载；L0–L2 分层 Prompt；5 种协作策略；实体-关系-事件记忆；Git Worktree 隔离思路 | `agents.go`、`prompt.go`、`team.go`、`memory.go` | 配置从 YAML 换成 JSON（标准库、零解析开销）；场景分类用关键词规则而非 LLM；并发上限 2、子任务有 token 帽；Worktree 换成 hostd 的 workspace 路径约束 |

**被主动放弃的**：所有 GUI（Chromium / Dart 引擎）、所有本地推理、所有跨设备组网、所有动态插件加载。这些不是「以后再做」，而是在 1 GB / 16 GB 的约束下**明确不做**——写进文档是为了防止后续自己把系统又养胖。

---

## 二、进程与内存模型

```
内核 + Armbian minimal        ~150 MB
zram（压缩内存，非 eMMC）     ~256 MB（按需）
chenxi (Go)                  ~25–40 MB RSS，软上限 128 MiB
chenxi-hostd (Rust)          ~3–8 MB RSS
页缓存 / 网络缓冲             ~150 MB
────────────────────────────────────
余量                          ~400 MB（留给突发与内核，不放模型）
```

硬机制：

1. `debug.SetMemoryLimit(128 << 20)` + `GOGC=50`：Go 堆到上限会主动 GC 而不是把盒子 OOM 掉。
2. 所有输入有帽：`max_message_bytes`、`max_tool_output_bytes`、`max_context_chars`、`max_index_entries`。
3. 事件日志 append-only，超过 `max_log_bytes` 触发 compaction（把旧事件压成摘要后截断），不是无限增长。
4. 倒排索引固定容量，超限淘汰最旧条目；快照是全量重写但有大小上限，避免 eMMC 写放大。
5. hostd 每连接一线程，但用 `active` 原子计数把**并发执行数硬限为 2**（`max_workers`），超限直接回 `hostd busy`；连接数本身也有上限。每次 exec 有超时与输出截断，不 spawn 无界任务。

---

## 三、UDS 协议（Go ↔ Rust）

Unix Domain Socket：`/run/chenxi/hostd.sock`。**每行一个扁平 JSON 对象，所有值都是字符串**，双方都用行分隔（NDJSON）。

请求：

```json
{"id":"17","op":"exec","cap":"exec","arg":"uname -a","mode":"workspace","approval":"allow"}
```

响应：

```json
{"id":"17","ok":"true","data":"Linux armbian ...","error":""}
```

| 字段 | 含义 |
|---|---|
| `id` | 递增请求号，用于匹配（对应 writer claim 思路） |
| `op` | `exec` / `read` / `write` / `net` / `approve` / `list` / `ping` |
| `cap` | 能力名，必须在 hostd 的 capability 白名单内 |
| `arg` | 参数（路径或命令） |
| `mode` | `readonly` / `workspace` / `full`，AI 能力档位 |
| `approval` | `allow` / `ask` / `deny`；`ask` 时 hostd 返回待审批 token |

**为什么用扁平字符串对象**：Rust 侧零依赖，一个 ~60 行的解析器即可覆盖全部协议，不需要 serde；Go 侧直接 `map[string]string` + `encoding/json`。字符串值也避免了数字/浮点解析的边界坑。换行与引号做转义，单行上限 64 KiB。

---

## 四、四层权限（hostd 内）

```
0. 进程沙箱   systemd: User=chenxi-host、ProtectSystem=strict、NoNewPrivileges、
              PrivateTmp、RestrictAddressFamilies、CapabilityBoundingSet=
1. Host 授权  capability 白名单（fs_read / workspace_write / exec / net_probe）
              由 hostd 自己的配置文件决定，Go 侧无法扩权
2. AI 档位    readonly → 只读；workspace → 仅 workspace 目录内写；
              full → 放宽 AI 限制，但不提升 0/1 层权限
3. 用户审批   ask → 生成一次性 token，必须由带外请求 approve 才执行；
              deny → 直接拒绝并记审计
```

三条不可破的不变式（写进 `AGENTS.md`，也让 Agent 每次会话都看见）：

- 加入会话不授予他人本地文件/终端/root 能力；
- `full` 只放宽 AI 档位，不创造操作系统没给的能力；
- 内部 sandbox 字段与 UI 不得被描述成真正的 OS 级隔离。

每次请求在**执行前**重新校验四层，不缓存授权结果（学 OpenClaw `AGENTS.md` 的「revalidate right before effect」）。所有请求与结果写 append-only 审计日志。

---

## 五、Agent Loop

```
入站(不可信) → 长度/字符清洗 → 单写者锁 → L0/L1/L2 装配
  → Provider(云模型) → 解析 tool call(JSON) → HostClient → hostd
  → 结果 append → 循环(≤ max_steps) → 回复 → 技能/记忆提炼
```

- 同一会话的 **L0 前缀逐字节稳定**：SOUL.md、AGENTS.md、硬约束只在启动时读一次；会话中途不改工具集、不重建 system prompt，保证 prompt cache 可复用（OpenClaw / Hermes 共同的不变式）。
- 历史只 append；只有超过 `max_context_chars` 才做一次压缩，压缩结果为新的一条摘要事件。
- 工具调用格式极简：模型输出 `{"tool":"exec","arg":"...","mode":"workspace"}`，解析失败按普通文本回复，不重试、不猜测。
- 每轮 `max_steps` 上限，防止Agent 在 1 GB 设备上无限自循环烧 token。

---

## 六、记忆与技能

- **事件**：`message` / `tool` / `memory` / `skill` / `summary`，append-only JSONL。
- **图谱**：实体-关系-事件三元组，内存 map + 快照，条目上限 20,000。用户纠正直接写入 `correction` 关系，下次装配 L0 时带出（对应璇玑「纠正一次永久生效」）。
- **检索**：中文按 bigram、英文按词构建倒排，简化 TF-IDF 打分，取 top-K 注入上下文（替代 Hermes FTS5）。
- **技能**：一次成功的多步工具序列被规则式提炼成模板，带版本号与使用计数；每次使用后更新 `last_used`，不额外调用 LLM（省内存与 token）。技能自进化只记「使用记录 + 人工修订」，不自动改写自身代码。

---

## 七、协作策略（限并发）

`serial` / `parallel` / `hierarchy` / `debate` / `pipeline` 五种语义保留璇玑的定义，但：

- 全局信号量 = `max_concurrency`（默认 2，最小 1）；
- 每个子任务有 `max_subagent_chars` 帽；
- `parallel` / `debate` 在 `max_concurrency=1` 时自动退化为 `serial` 并在回复里说明；
- 策略选择由规则 + 用户显式 `@team:<strategy>` 触发，不引入额外的「分类模型」。

这是对璇玑快照里「五种策略缺少端到端运行证据」的保守回应：语义先落地，规模化并发在这块板子上不承诺。

---

## 八、可验证性 / 本次交付边界

- Go 侧仅用标准库，Rust 侧仅用 `std`，`go.mod` 无 `require`、`Cargo.toml` 无 `[dependencies]`。
- Go 侧不需要 cgo、Rust 侧零 crate；推荐在 aarch64 主机（Armbian 板子或 GitHub `ubuntu-24.04-arm` runner）上 `make build-local` 原生编译，避开 x86 → aarch64 交叉链接器的 sysroot 差异。离线也能在板子上编译。
- **本次只交付源码与设计，未在真机烧录、未压测、未做安全审计**；内存数字是设计预算，不是实测值。上板后请用 `systemctl status` + `ps -o rss` + `free -m` 实测并回写 `config.json` 的 budgets。

---

## 九、渠道接入

### 为什么是「先 ACK，再异步回复」

飞书 challenge 要求 1 秒内回显，企微 5 秒收不到就断连重试，QQ 回调同样有超时；而 Agent Loop 里一次云端模型调用经常十几秒。把 LLM 挡在回调里必然超时重试，重试又会造成重复回复。所以：

```
回调 → 验签/解密 → 去重 → 入有界队列 → 立即 200
                                  ↓ worker（数量 = max_concurrency ≤ 2）
                        App.Handle → 平台 OpenAPI 主动发消息
```

Go 侧不新增长连接、不维护 per-user 会话状态、不落平台消息体，只留一个 256 条的去重集合与容量 4 的队列。队列满时返回 `503 + Retry-After`，且**故意不记去重键**，让平台重试时还有机会入队。

### 零依赖的验签矩阵

| 平台 | 算法 | 用到的标准库 |
|---|---|---|
| 飞书 | `SHA256(EncryptKey)` → AES-256-CBC，IV 在密文前 16 字节；或 Verification Token 常量比对 | `crypto/sha256` `crypto/aes` `crypto/cipher` |
| 企业微信 | `sha1(sort(token,ts,nonce,encrypt))`；`Base64Decode(AESKey+"=")` → AES-256-CBC，IV = key[:16]，PKCS7/32；明文 `16B+4B大端长度+XML+receiveid` | `crypto/sha1` `crypto/aes` `encoding/xml` |
| QQ | Bot Secret 重复填充成 32 字节 Ed25519 seed；验签体 `timestamp + 原始 body`；回调验证签 `event_ts + plain_token` | `crypto/ed25519` |
| 通用 Webhook | `HMAC-SHA256(secret, raw body)` | `crypto/hmac` |
| 微信网关 | `Authorization: Bearer <共享密钥>` 常量时间比对 | `crypto/subtle` |
| 微信 iLink 协议 | **不在晨曦内**：由外部网关负责 | — |

**不变式**：未配置验签凭据的渠道拒绝启动；所有渠道只做「验签/握手 → 提取文本 → 回复」，不得 import `os/exec`、不得直接调 `Provider.Chat`；入站一律经 `App.Handle` 清洗。护栏脚本把这三条做成 CI 断言。

### 渠道也是 1 GB 预算的一部分

- worker 数量与 Agent Loop 共享 `max_concurrency`，不另起线程池；
- `tokenCache` 单飞取平台 access_token（有效期 7200s，提前 5 分钟刷新），不重复请求；
- 所有平台调用走同一个带超时的 `http.Client`，响应体有上限；
- 审计只记参数长度，不记消息原文；通知 URL、密钥全走环境变量。

### 微信的诚实边界

上一版写过「个人微信没有官方 bot API、明确不做」，这个说法太绝对，本次按参考仓库做了修正：微信开放社区确有 [clawbot 相关接口](https://developers.weixin.qq.com/doc/aispeech/knowledge/openapi/Clawbotrelated.html) 文档（二维码登录、状态轮询、通道重置），`ilinkai.weixin.qq.com` 是腾讯域名；但该文档未说明适用范围、接入资格与自动化回复授权，同时有权威分析明确反驳「官方开放个人微信 Bot、合法无封号」的营销叙事，并援引《微信软件许可及服务协议》对第三方自动化操作的禁止条款。

所以晨曦的「微信」有三层：

1. **合规首选**：企业微信自建应用（微信用户可进外部群，双向）+ 服务号推送出口（单向）。
2. **可选旁路**：参考 [xuanji-weixin-gateway](https://github.com/huangyong7878/xuanji-weixin-gateway) 的 `callback` 模式，加一个零依赖 HTTP 适配器。扫码登录 / `getupdates` 长轮询 / `bot_token` / 附件落盘全留在**外部机器**，晨曦只验 Bearer 共享密钥、归一化事件、调 `/send` 回 `context_token`。默认关闭、合规自担。
3. **明确不做**：协议逆向（iPad 协议）、PC Hook、DLL 注入，以及把 Node ≥22 + `better-sqlite3` 搬上 1 GB 的 S905M。

这是对「参考一下」的保守回应：**借鉴它的上游中立设计与幂等契约，不引入它的运行时**。

### UDS 联调补上了什么

上一版只做静态语法检查，两个进程能不能真配合没验证。本次已把 `scripts/integration-test.sh` 编排进 CI：它会编译并启动真实 `chenxi-hostd`，用 Go 侧 `HostClient` 走完整协议（本地沙盒缺 Go/Rust 工具链，待推送后由 Actions 实际执行）：ping / list / read / exec / 路径穿越拒绝 / shell 元字符拒绝 / 非白名单命令拒绝 / readonly 写拒绝 / Host 白名单拒绝 / `deny` / `ask → approve → token 一次性 → 文件落盘` / 审计日志非空且不泄露参数原文。顺带修了一个真实缺陷：`approve` 回放路径原先绕过 `active` 并发闸，现已把并发计数移入 `execute()` 覆盖全部执行路径，并给待审批 token 加 5 分钟过期清理。

**仍未验证**：飞书 / 企业微信 / QQ 的真实线上回调、外部微信网关的扫码登录与投递、平台限流与重试行为、QQ 审核流程、S905M 真机。协议字段按各家官方文档与参考仓库 README 实现，但没做过真实平台联调。微信网关的 `callback` 非 2xx 重试行为在参考仓库 README 中未承诺，过载时可能丢消息；`inbox` 拉取模式是可能的后续扩展，当前未实现。
