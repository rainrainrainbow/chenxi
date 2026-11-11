## 改动

<!-- 一句话说清改了什么、为什么 -->

## 是否触及架构护栏

- [ ] 没有引入 Go / Rust 第三方依赖
- [ ] 没有在 Go 侧直接调用 `os/exec` 或写业务文件（副作用都走 hostd）
- [ ] 没有启用本地推理 / cgo / GUI
- [ ] `config.json` 的硬预算未被放宽（并发 ≤2、Go 堆 ≤256 MiB）
- [ ] 新增能力先考虑做成 hostd capability 或技能，而不是往 Agent Loop 堆分支
- [ ] 改渠道时：验签/解密在最前、回调只 ACK 不阻塞、密钥只走环境变量、入站经 `Submit → App.Handle`
- [ ] 改 hostd 时跑过 `make test-integration`（真实双进程联调）

## 验证

- [ ] `bash scripts/check-guardrails.sh` 通过
- [ ] `go test ./... -count=1` 通过
- [ ] `cd hostd && cargo test` 通过
- [ ] `make build-arm64` + `bash scripts/verify-artifacts.sh` 通过
- [ ] 在 S905M 上实测过 `ps -o rss` / `free -m`（如涉及内存）

## 风险与回滚

<!-- 说明最坏情况、以及如何回滚 -->
