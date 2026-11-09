#!/usr/bin/env bash
# 编译完成后校验：真的是 aarch64、真的没超体积预算。
set -euo pipefail

fail() { echo "::error::$1"; exit 1; }
command -v readelf >/dev/null || fail "readelf 不可用"

for f in dist/chenxi dist/chenxi-hostd; do
  [[ -f "$f" ]] || fail "缺少产物 $f"
  [[ -s "$f" ]] || fail "产物为空 $f"
  if ! readelf -h "$f" | grep -q 'AArch64'; then
    fail "$f 不是 AArch64 二进制（readelf Machine 字段）"
  fi
done

go_sz=$(stat -c%s dist/chenxi)
rs_sz=$(stat -c%s dist/chenxi-hostd)
max_go=$((20 * 1024 * 1024))
max_rs=$((8 * 1024 * 1024))

(( go_sz > 0 && go_sz < max_go )) || fail "Go 二进制体积越界：${go_sz}B（上限 ${max_go}B）"
(( rs_sz > 0 && rs_sz < max_rs )) || fail "Rust 二进制体积越界：${rs_sz}B（上限 ${max_rs}B）"

# Go 产物必须是静态链接；Rust 允许动态链接系统 glibc，但不应链接额外私有库。
if readelf -l dist/chenxi | grep -q 'INTERP'; then
  fail "Go 产物应为静态链接，却出现了 INTERP 段"
fi

echo "OK  chenxi=${go_sz}B (static)  chenxi-hostd=${rs_sz}B  target=aarch64"
readelf -h dist/chenxi | grep -E 'Class|Machine' | sed 's/^/    /'
