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
```

交叉编译（需要 `gcc-aarch64-linux-gnu` 与 Rust 的
`aarch64-unknown-linux-gnu` target）：

```bash
make build-arm64 && bash scripts/verify-artifacts.sh
```

也可以直接在 Armbian 板子上 `make build-local`，省掉交叉链接器。

## CI

- `ci.yml`：每次 push / PR 跑护栏 + 单元测试 + aarch64 交叉编译，产物上传为 artifact。
- `release.yml`：推送 `v*` tag（或手动 dispatch）生成 Release 与 SHA256SUMS。
- `nightly.yml`：每周跑竞态检测、govulncheck 与工具链漂移复核。

提交信息用中文或英文都行，但请写清「为什么」。
