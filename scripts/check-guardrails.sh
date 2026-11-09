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

echo "[晨曦架构护栏] 全部通过"
