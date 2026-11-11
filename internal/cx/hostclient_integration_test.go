package cx

import (
	"context"
	"os"
	"strings"
	"testing"
)

// TestIntegrationUDS 起一个真实的 chenxi-hostd 进程，用 Go 侧 HostClient
// 走完整 UDS 协议。只有设置 CHENXI_INTEGRATION_SOCKET 时才运行，
// 由 scripts/integration-test.sh 在 CI 里拉起；平时 go test ./... 会 Skip。
func TestIntegrationUDS(t *testing.T) {
	sock := os.Getenv("CHENXI_INTEGRATION_SOCKET")
	if sock == "" {
		t.Skip("设置 CHENXI_INTEGRATION_SOCKET 才跑真实 UDS 联调（见 scripts/integration-test.sh）")
	}
	cfg := &Config{
		Socket: sock,
		Host:   HostConfig{TimeoutSec: 5},
	}
	h := NewHostClient(cfg)
	ctx := context.Background()

	call := func(op, capName, arg, mode, approval string) HostReply {
		t.Helper()
		r, err := h.Call(ctx, op, capName, arg, mode, approval)
		if err != nil {
			t.Fatalf("%s/%s RPC 失败：%v", op, capName, err)
		}
		return r
	}
	denied := func(op, capName, arg, mode, approval, why string) {
		t.Helper()
		r := call(op, capName, arg, mode, approval)
		if r.OK {
			t.Fatalf("期望被拒绝（%s），实际通过：%q", why, r.Data)
		}
	}

	// 1. 进程存活 + 能力白名单
	if err := h.Ping(ctx); err != nil {
		t.Fatal("hostd ping 失败：", err)
	}
	r := call("list", "", "", "readonly", "allow")
	for _, name := range []string{"read", "write", "exec", "net"} {
		if !strings.Contains(r.Data, name) {
			t.Fatalf("hostd 能力白名单缺少 %s：%q", name, r.Data)
		}
	}

	// 2. 只读读取预置文件
	r = call("read", "read", "hello.txt", "readonly", "allow")
	if !r.OK || !strings.Contains(r.Data, "chenxi-integration-ok") {
		t.Fatalf("读取 workspace 文件失败：ok=%v data=%q err=%q", r.OK, r.Data, r.Error)
	}

	// 3. 第 2 层校验：路径穿越、shell 元字符、命令白名单、readonly 档位
	denied("read", "read", "../etc/passwd", "readonly", "allow", "路径穿越")
	denied("exec", "exec", "uname -a; id", "readonly", "allow", "shell 元字符")
	denied("exec", "exec", "rm -rf /tmp/x", "readonly", "allow", "非白名单命令")
	denied("write", "write", "a.txt=hello", "readonly", "allow", "readonly 档位")

	r = call("exec", "exec", "uname -a", "readonly", "allow")
	if !r.OK || !strings.Contains(r.Data, "Linux") {
		t.Fatalf("白名单命令执行失败：ok=%v data=%q err=%q", r.OK, r.Data, r.Error)
	}

	// 4. 第 1 层校验：Host 白名单外的 capability
	denied("exec", "sudo", "whoami", "readonly", "allow", "Host 授权白名单")

	// 5. 第 3 层校验：deny / ask / approve 完整链路
	denied("read", "read", "hello.txt", "readonly", "deny", "deny 策略")

	r = call("write", "write", "approved.txt=approved-content", "workspace", "ask")
	if r.OK || r.Error != "approval_required" || r.Data == "" {
		t.Fatalf("ask 应返回一次性 token，实际：ok=%v data=%q err=%q", r.OK, r.Data, r.Error)
	}
	token := r.Data

	// 审批前文件不应存在
	r = call("read", "read", "approved.txt", "readonly", "allow")
	if r.OK {
		t.Fatal("未经审批，写操作已提前生效")
	}

	// 带外批准 → 真正执行
	r = call("approve", "", token, "readonly", "allow")
	if !r.OK {
		t.Fatalf("approve 执行失败：%q", r.Error)
	}

	// token 一次性：重放必须失败
	denied("approve", "", token, "readonly", "allow", "审批 token 重放")

	// 文件确实落盘，且内容正确
	r = call("read", "read", "approved.txt", "readonly", "allow")
	if !r.OK || !strings.Contains(r.Data, "approved-content") {
		t.Fatalf("审批后写入未生效：ok=%v data=%q err=%q", r.OK, r.Data, r.Error)
	}

	// 6. 审批 token 过期清理不会误伤：新 ask 仍能生成 token
	r = call("exec", "exec", "date", "readonly", "ask")
	if r.OK || r.Error != "approval_required" || r.Data == "" || r.Data == token {
		t.Fatalf("新的 ask 应生成不同 token：ok=%v data=%q err=%q", r.OK, r.Data, r.Error)
	}
}
