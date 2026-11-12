#!/usr/bin/env bash
# 把 1 GB 机器的设计约束变成 CI 里会失败的断言，而不是文档里的一句话。
set -euo pipefail

fail() { echo "::error::$1"; exit 1; }
ok()   { echo "  ✓ $1"; }

echo "[晨曦架构护栏]"

# 1. Go 侧零第三方依赖（go.mod 里不能出现 require）
if grep -Eq '^[[:space:]]*require([[:space:]]|\()' go.mod; then
  fail "go.mod 出现 require：晨曦的 Go 运行时只允许标准库"
fi
ok "go.mod 无 require"

# 2. Rust 侧零 crate（[dependencies] 段必须为空）
awk '
  /^\[dependencies\]/ { in_deps = 1; next }
  /^\[/              { in_deps = 0 }
  in_deps && NF && $0 !~ /^[[:space:]]*#/ {
    print "  -> " $0; bad = 1
  }
  END { exit bad }
' hostd/Cargo.toml || fail "hostd/Cargo.toml 的 [dependencies] 不为空"
ok "hostd 零 crate"

# 3. Go 侧不得直接执行外部命令或引入 cgo
if grep -Rqs --include='*.go' '"os/exec"' cmd internal; then
  fail "Go 侧出现 os/exec：所有副作用必须经 UDS 交给 chenxi-hostd"
fi
ok "Go 侧无 os/exec"

if grep -Rqs --include='*.go' 'import "C"' cmd internal; then
  fail "Go 侧出现 cgo：交叉编译会失去可复现性"
fi
ok "Go 侧无 cgo"

# 4. 本地大模型必须被明确拒绝
grep -q 'provider.kind=local 被禁用' internal/cx/config.go \
  || fail "本地推理的硬拒绝逻辑被移除"
grep -q '1 GB RAM 下禁用本地推理' internal/cx/provider.go \
  || fail "Provider 层的本地推理拒绝被移除"
ok "本地推理明确禁用"

# 5. 可机读宪法与硬预算必须存在
for f in SOUL.md AGENTS.md agents.json config.json; do
  [[ -s "$f" ]] || fail "缺少或空文件：$f"
done
grep -q 'go_mem_limit_mib' config.json || fail "config.json 缺少堆内存上限"
grep -q 'max_concurrency' config.json  || fail "config.json 缺少并发上限"
ok "宪法与硬预算齐备"

# 6. hostd 必须实现四层校验（审批 / 白名单 / 档位 / 实现）
for pat in 'approval == "deny"' 'host allowlist' 'cap.check(mode' 'cap.run(arg'; do
  grep -q "$pat" hostd/src/main.rs || fail "hostd 缺少权限校验环节：$pat"
done
ok "hostd 四层权限链路完整"

# 7. 渠道适配器：只做验签与文本提取，不持能力
if grep -Rqs --include='channel_*.go' '"os/exec"' internal/cx; then
  fail "渠道适配器出现 os/exec：所有副作用必须仍走 hostd"
fi
ok "渠道适配器无副作用能力"

if grep -Rqs --include='channel_*.go' '\.Chat(' internal/cx; then
  fail "渠道绕过了 App.Handle，直接调用模型"
fi
ok "所有渠道经 Gateway 单一入口"

# 8. 四家接入的验签/解密原语必须齐备（缺一个就等于裸奔公网）
for pat in 'ed25519.Verify' 'wecomSignature' 'hmacSHA256Hex' 'decryptFeishu' 'decryptWecom' 'Authorization.*Bearer'; do
  grep -Rqs --include='*.go' "$pat" internal/cx || fail "渠道验签/解密实现缺失：$pat"
done
ok "飞书 / 企微 / QQ / 通用 Webhook / 微信网关 验签链路齐备"

# 微信网关适配器不得把 Node 运行时或附件文件搬进本机，也不得自己实现微信协议
if grep -qs 'ilink/bot/' internal/cx/channel_weixin.go; then
  fail "晨曦不得直接实现 iLink 协议：协议部分交给外部网关"
fi
grep -q '不实现微信协议' internal/cx/channel_weixin.go \
  || fail "微信网关适配器缺少边界声明"
grep -q 'shared_secret_env' internal/cx/channel_config.go \
  || fail "微信网关缺少共享密钥配置"
ok "微信网关仅做外部 HTTP 对接，协议与运行时留在旁路"

grep -q 'json:"app_secret_env"' internal/cx/channel_config.go \
  || fail "渠道配置丢失「密钥只写环境变量名」的约定"
ok "渠道密钥只走环境变量"

# 9. UDS 双进程联调必须存在且被 CI 调用
[[ -f scripts/integration-test.sh ]] || fail "缺少 UDS 联调脚本"
grep -q 'TestIntegrationUDS' scripts/integration-test.sh || fail "联调脚本未调用集成测试"
grep -q 'integration' .github/workflows/ci.yml || fail "CI 未编排 UDS 联调 job"
ok "Go ↔ hostd 联调已纳入 CI"

echo "[晨曦架构护栏] 全部通过"
