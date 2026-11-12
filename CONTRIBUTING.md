# 参与晨曦

## 先读三份文件

`SOUL.md`（人格）、`AGENTS.md`（运行宪法）、`DESIGN.md`（取舍与协议）。
它们不是装饰：前两份会被注入每次会话的 L0 前缀，第三份定义了 UDS 协议。

## 三条规矩

1. **别把系统养胖。** 这台机器只有 1 GB RAM。新能力优先做成
   `hostd` 的一个 capability 或一条技能；引入第三方库前，先说明它省下的
   内存少于它带来的常驻开销。
2. **副作用只有一个出口。** Go 侧不 `os/exec`、不直接写业务文件。
   所有外部动作经 `/run/chenxi/hostd.sock` 交给 Rust 守护进程。
3. **前缀稳定。** 同一会话内不重建 system prompt、不改工具集、不重载记忆；
   历史只 append，唯一例外是超预算时的一次压缩。

## 本地验证

```bash
bash scripts/check-guardrails.sh
go vet ./... && go test ./... -count=1
cd hostd && cargo check --all-targets && cargo test
make test-integration     # 起真实 chenxi-hostd，跑 Go ↔ UDS 端到端
```

新增渠道时：验签/解密放在 `Handle` 最前面，回调里绝不阻塞等模型；所有文本经 `ChannelManager.Submit` → `App.Handle`；密钥只写环境变量名；平台调用复用 `mgr.doJSON` 与 `tokenCache`。个人微信协议逆向 / PC Hook 不接受 PR；ClawBot/iLink 协议实现请留在外部网关仓库，晨曦这边只收 HTTP 适配器。

交叉编译（需要 `gcc-aarch64-linux-gnu` 与 Rust 的
`aarch64-unknown-linux-gnu` target）：

```bash
make build-arm64 && bash scripts/verify-artifacts.sh
```

也可以直接在 Armbian 板子上 `make build-local`，省掉交叉链接器。

## CI

- `ci.yml`：每次 push / PR 跑护栏 + 单元测试 + 真实 hostd 的 UDS 双进程联调 + 原生 arm64 编译，产物上传为 artifact。
- `release.yml`：推送 `v*` tag（或手动 dispatch）生成 Release 与 SHA256SUMS。
- `nightly.yml`：每周跑竞态检测、govulncheck 与工具链漂移复核。

提交信息用中文或英文都行，但请写清「为什么」。
