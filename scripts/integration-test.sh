#!/usr/bin/env bash
# 真实起一个 chenxi-hostd 进程，用 Go 侧 HostClient 跑完整 UDS 协议。
# 验证：ping / list / read / exec / 四层权限拒绝 / ask→approve→一次性 token。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
TMP="$(mktemp -d)"
HOSTD_PID=""

cleanup() {
  if [ -n "$HOSTD_PID" ] && kill -0 "$HOSTD_PID" 2>/dev/null; then
    kill "$HOSTD_PID" 2>/dev/null || true
    wait "$HOSTD_PID" 2>/dev/null || true
  fi
  rm -rf "$TMP"
}
trap cleanup EXIT

mkdir -p "$TMP/ws" "$TMP/log"
printf 'chenxi-integration-ok\n' > "$TMP/ws/hello.txt"

# hostd 的配置是「所有值都是字符串」的扁平 JSON。
cat > "$TMP/hostd.json" <<EOF
{"socket":"$TMP/hostd.sock","workspace":"$TMP/ws","audit":"$TMP/log/audit.jsonl","caps":"read,write,exec,net","workers":"2","timeout_sec":"5","max_output_bytes":"65536"}
EOF

echo "[integration] 编译 hostd（debug）"
(cd "$ROOT/hostd" && cargo build --quiet)

echo "[integration] 启动 hostd：$TMP/hostd.sock"
"$ROOT/hostd/target/debug/chenxi-hostd" --config "$TMP/hostd.json" \
  > "$TMP/log/hostd.stderr" 2>&1 &
HOSTD_PID=$!

for _ in $(seq 1 100); do
  if [ -S "$TMP/hostd.sock" ]; then
    break
  fi
  if ! kill -0 "$HOSTD_PID" 2>/dev/null; then
    echo "!! hostd 提前退出" >&2
    cat "$TMP/log/hostd.stderr" >&2
    exit 1
  fi
  sleep 0.1
done
if [ ! -S "$TMP/hostd.sock" ]; then
  echo "!! hostd 未创建 socket" >&2
  cat "$TMP/log/hostd.stderr" >&2
  exit 1
fi

echo "[integration] Go ↔ hostd 端到端"
CHENXI_INTEGRATION_SOCKET="$TMP/hostd.sock" \
CHENXI_INTEGRATION_WS="$TMP/ws" \
  go test ./internal/cx -run '^TestIntegrationUDS$' -count=1 -v

echo "[integration] 校验审计日志"
if [ ! -s "$TMP/log/audit.jsonl" ]; then
  echo "!! 审计日志为空，hostd 未记录请求" >&2
  exit 1
fi
echo "  审计事件数：$(wc -l < "$TMP/log/audit.jsonl")"
if grep -q 'approved-content' "$TMP/log/audit.jsonl"; then
  echo "!! 审计日志泄露了参数原文" >&2
  exit 1
fi

echo "[integration] OK：UDS 协议、四层权限、审批链路全部通过"
