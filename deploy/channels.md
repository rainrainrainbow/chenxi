# 晨曦 · 渠道接入指南

> 适用本次渠道扩展后的版本。所有渠道默认关闭，密钥只走环境变量，不写进 `config.json`。

## 一、先把边界说清楚

| 渠道 | 双向对话 | 合规路径 | 备注 |
|---|---|---|---|
| **飞书** | ✅ | 企业自建应用机器人 + 事件订阅（HTTP 回调） | 最稳，推荐先用它跑通 |
| **企业微信** | ✅ | 自建应用「接收消息」回调 + 应用消息接口 | 微信用户可加入企微外部群，这是「微信」的合规双向路径 |
| **QQ** | ✅ | QQ 开放平台官方机器人（Webhook 模式） | 需企业主体、IP 白名单、审核；沙箱环境可先测 |
| **通用 Webhook** | ✅ | 自定义 HMAC-SHA256 回调 | 接 n8n / Home Assistant / Telegram 桥 / 自建前端 |
| **通知出口** | 单向 | Server 酱 / PushPlus（微信服务号推送）、群机器人、Bark | 把晨报与告警推到微信 |
| **个人微信协议登录** | ❌ | — | 无官方 bot API；iPad 协议 / PC Hook 违规且封号，晨曦明确不做 |

网上流传的「微信个人号 Bot API」未见官方文档支持，本项目不纳入。要双向对话就用企业微信，要收通知就用服务号推送。

---

## 二、所有渠道共用的机制

平台回调普遍 **1~5 秒超时**，而 LLM 调用动辄十几秒。晨曦统一：

```
回调 → 验签/解密 → 去重 → 入有界队列 → 立即 200 ACK
                                    ↓（后台 worker，并发 ≤2）
                          App.Handle → 平台 OpenAPI 主动回消息
```

- **队列**：容量 `2 × max_concurrency`（默认 4），worker 数 = `max_concurrency`（默认 2），与 Agent Loop 共享同一并发预算。
- **去重**：`event_id` / `MsgId` 内存有界集合（默认 256 条，FIFO 淘汰）；重复推送静默 ACK，不重复回复。
- **队列满**：返回 **503 + `Retry-After: 2`**，且**不记录去重键**，让平台重试时仍能入队。这是 1 GB 机器上明确的降级行为：持续过载时平台重试耗尽后会丢消息。
- **入站不可信**：任何渠道文本都经 `App.Handle` 的 `Sanitize`，与工具输出一样只当数据、不当指令。

### 公网暴露（必读）

晨曦沿用「单可信操作者」信任模型，**没有多租户隔离**。回调要被公网访问，就必须：

1. **不要**把 `127.0.0.1:7788` 直接开公网。用 Caddy / Nginx 反代到本机，只暴露 `/v1/channel/*` 路径。
2. **必须 HTTPS**（三家平台都要求）。
3. **反向代理必须保留原始请求体**：QQ 的 Ed25519 验签签的是 `timestamp + 原始 body`，任何重新序列化 / 改写都会验签失败。
4. 建议额外限制来源 IP 段、给回调路径加 `RateLimit`。
5. 未配置验签凭据的渠道**拒绝启动**，不存在「先跑起来再说」的裸奔状态。

Caddy 示例：

```caddyfile
ai.example.com {
    @channel path /v1/channel/*
    reverse_proxy @channel 127.0.0.1:7788
    # 其余路径一律不暴露
    respond "not found" 404
}
```

---

## 三、飞书（推荐先跑通）

1. 开放平台建**企业自建应用** → 启用「机器人」能力 → 发布版本。
2. 「事件与回调」→ 订阅方式选**将事件发送至开发者服务器**，请求地址填 `https://ai.example.com/v1/channel/feishu`，订阅 `im.message.receive_v1`。
3. 「加密策略」里**至少开一项**：Encrypt Key（AES 加密）或 Verification Token。生产建议开 Encrypt Key。
4. 权限勾 `im:message` 或 `im:message:send_as_bot`（主动回消息必需）。
5. `config.json` 里 `channels.feishu.enabled = true`，填 `app_id` / `app_secret` 对应的环境变量名。

环境变量：

```bash
CHENXI_FEISHU_APP_ID=cli_xxx
CHENXI_FEISHU_APP_SECRET=xxx
CHENXI_FEISHU_ENCRYPT_KEY=xxx          # 与后台 Encrypt Key 一致
# 或者走 Verification Token：把 config 里的 encrypt_key_env 置空，
# 改填 verify_token_env=CHENXI_FEISHU_VERIFY_TOKEN
```

> 生产上把这些写进 `/etc/chenxi.env`（`chmod 600`）。`chenxi.service` 已配
> `EnvironmentFile=-/etc/chenxi.env`，不需要改 systemd unit；改完 `systemctl restart chenxi`。

实现要点：challenge 在 1 秒内原样返回；开启 Encrypt Key 时 body 为 `{"encrypt": base64(iv+AES)}`，密钥 = `SHA256(EncryptKey)`，IV = 密文前 16 字节。回复走 `POST /open-apis/im/v1/messages?receive_id_type=chat_id`，用 `tenant_access_token`，并以 `message_id` 作 `uuid` 幂等。

---

## 四、企业微信（微信的合规双向路径）

1. 管理后台 → 自建应用 → 「接收消息」→ 设置 API 接收：URL 填 `https://ai.example.com/v1/channel/wecom`，记下 Token 与 43 字符 EncodingAESKey。
2. 记下 CorpID、AgentID、应用 Secret。
3. `config.json` 里 `channels.wecom.enabled = true`，填 `corp_id` / `agent_id`。

```bash
CHENXI_WECOM_TOKEN=xxx
CHENXI_WECOM_AES_KEY=xxx              # 43 字符 EncodingAESKey
CHENXI_WECOM_SECRET=xxx
```

实现要点：后台保存时会发 **GET** 带 `msg_signature/timestamp/nonce/echostr`，晨曦验签（`sha1(sort(token,ts,nonce,encrypt))`）后解密 echostr 并**原样回显明文**（不加引号、BOM、换行）。消息推送是 XML，`Encrypt` 字段 AES-256-CBC 解密（key = `Base64Decode(AESKey+"=")`，IV = key 前 16 字节，PKCS7 按 32 字节分组），明文为 `16B随机 + 4B大端长度 + XML + receiveid(corpid)`，并校验 receiveid。5 秒内 ACK 空包，回复走 `cgi-bin/message/send` 异步送达。

> 微信用户可以在企微外部群里说话，机器人能收到并回复——这就是「微信接入」的合规形态。个人微信好友/普通微信群没有官方通道。

---

## 五、QQ 官方机器人

1. QQ 开放平台建机器人，用**企业主体**；配置 Webhook 回调地址 `https://ai.example.com/v1/channel/qq`，订阅 C2C 单聊 / 群 @ 事件。
2. 记下 AppID 与 Bot Secret，配置公网出口 IP 白名单；先用**沙箱环境**联调（`channels.qq.sandbox = true`）。
3. `channels.qq.enabled = true`。

```bash
CHENXI_QQ_APP_ID=1024xxxxx
CHENXI_QQ_SECRET=xxx
```

实现要点：所有回调先验 `X-Signature-Ed25519` / `X-Signature-Timestamp`，Bot Secret 重复填充到 32 字节作为 Ed25519 seed 派生公钥，验签体 = `timestamp + 原始 body`。回调地址验证（`op=13`）用同一私钥签 `event_ts + plain_token` 并返回 hex。回复走 `POST /v2/users/{openid}/messages` 或 `/v2/groups/{group_openid}/messages`，头 `Authorization: QQBot <token>`，带 `msg_id` 作被动回复凭证（5 分钟内有效）。

`api_base` 可配：正式 `https://api.bot.qq.com`，沙箱 `https://sandbox.api.sgroup.qq.com`（开 `sandbox=true` 会自动切换）。QQ 平台对主体、白名单、审核、场景权限限制较多，若后台版本字段与本文不一致，**以 QQ 开放平台官方文档为准**，不要凭报错猜字段。

---

## 六、通用 Webhook（覆盖「等」）

任何能发 HTTP 回调的系统都能接：

```
POST /v1/channel/webhook
X-Chenxi-Signature: sha256=<hex( HMAC-SHA256(secret, raw_body) )>
Content-Type: application/json

{"event_id":"唯一ID","text":"帮我看看磁盘","target":"可选","reply_url":"可选"}
```

- `reply_url` 非空时，回复 POST 到它（`{"target","text"}`）；否则走通知出口。
- `event_id` 建议给，用于排重。
- `channels.webhook.secret_env` 必填且环境变量必须存在，否则拒绝启动。

---

## 七、单向通知出口（把晨报推到微信）

```json
"channels": { "notify": { "enabled": true, "kind": "serverchan", "url_env": "CHENXI_NOTIFY_URL", "title": "晨曦" } }
```

| kind | URL 环境变量填什么 | 备注 |
|---|---|---|
| `serverchan` | `https://sctapi.ftqq.com/<KEY>.send` | 微信服务号推送 |
| `pushplus` | `http://www.pushplus.plus/send` | 另设 `CHENXI_NOTIFY_TOKEN` |
| `feishu_bot` | 飞书群自定义机器人 Webhook | 群机器人只能发，不能收 |
| `wecom_bot` | 企微群机器人 Webhook | 同上 |
| `bark` | Bark 推送地址 | iOS |
| `custom` | 任意接收 `{"title","text"}` 的 HTTP 端点 | 自建 |

把定时任务推到微信，只需把 `config.json` 里 cron 的 `target` 从 `cli` 改成 `notify`：

```json
{"id":"morning","spec":"daily 07:30","text":"今日简报…","target":"notify"}
```

---

## 八、验收清单

```bash
curl -s 127.0.0.1:7788/v1/status
# 应看到 "channels":["feishu",...] 与 "channel_queue":0

# 本地起反代后，用平台后台的「验证」按钮触发 challenge / URL 验证
# 发一条文本，观察：回调立刻 200，几秒后收到回复
# 重复投递同一 event_id，应只回一次

bash scripts/check-guardrails.sh
make test-integration        # Go ↔ hostd 真实联调，与渠道无关但一起跑
```

**仍然未验证**：三家平台的真实线上回调（需要你的应用凭据与公网域名）、平台限流与重试行为、QQ 审核流程。代码里的协议字段按各家官方文档实现，但**没有真机/真实平台联调过**。

## 参考

- 飞书开放平台：事件订阅、Encrypt Key 加密、`im/v1/messages`
- 企业微信开发者中心：加解密方案说明、获取 access_token、发送应用消息
- QQ 开放平台：Webhook 方式、安全和授权（Ed25519）、API 调用指南、获取访问凭证
