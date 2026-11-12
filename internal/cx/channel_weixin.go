package cx

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
)

// 微信（经外部 weixin-gateway）适配器。
//
// 这个适配器**不实现微信协议**。个人微信的 ClawBot/iLink 通道由外部独立网关
// （Node ≥22 + better-sqlite3，扫码登录 + getupdates 长轮询）负责；晨曦只做
// 一个零依赖的 HTTP 客户端，避免把 Node 运行时和 SQLite 搬上 1 GB 的 S905M。
//
// 链路：
//   微信 → 外部 gateway → POST /v1/channel/weixin (Bearer 共享密钥)
//        → 去重 → 有界队列 → 立即 2xx ACK
//        → App.Handle → POST gateway /send（带 context_token）→ 微信
//
// 合规边界：iLink/ClawBot 接口的适用范围与自动化回复授权在官方文档中未明示，
// 本适配器默认关闭，启用前请自行核对《微信软件许可及服务协议》与平台条款，
// 风险由操作者承担。晨曦明确不做协议逆向 / PC Hook。

type weixinChannel struct {
	mgr *ChannelManager
	cfg *WeixinGatewayConf
}

func validateWeixinGateway(c *WeixinGatewayConf) error {
	if c.Path == "" {
		return fmt.Errorf("weixin_gateway.path 未配置")
	}
	if c.SharedSecretEnv == "" {
		return fmt.Errorf("微信网关适配器必须配置 shared_secret_env，拒绝无鉴权回调")
	}
	if getenv(c.SharedSecretEnv) == "" {
		return fmt.Errorf("微信网关共享密钥环境变量 %s 未设置", c.SharedSecretEnv)
	}
	if !strings.HasPrefix(c.GatewayBase, "http://") && !strings.HasPrefix(c.GatewayBase, "https://") {
		return fmt.Errorf("weixin_gateway.gateway_base 必须是 http/https 地址")
	}
	if strings.Contains(c.GatewayBase, "example.com") {
		return fmt.Errorf("weixin_gateway.gateway_base 还是示例值，请改成实际网关地址")
	}
	return nil
}

func newWeixinChannel(m *ChannelManager, c *WeixinGatewayConf) Channel {
	return &weixinChannel{mgr: m, cfg: c}
}

func (c *weixinChannel) Name() string { return "weixin-gateway" }

// 事件结构与外部网关 README 的 upstream event schema 对齐。
type weixinEvent struct {
	Type         string           `json:"type"`
	AccountID    string           `json:"account_id"`
	EventID      string           `json:"event_id"`
	ChatID       string           `json:"chat_id"`
	UserID       string           `json:"user_id"`
	Text         string           `json:"text"`
	ContextToken string           `json:"context_token"`
	ChatType     string           `json:"chat_type"`
	Attachments  []weixinAttachment `json:"attachments"`
	Raw          json.RawMessage  `json:"raw"`
}

type weixinAttachment struct {
	Kind     string `json:"kind"`
	Path     string `json:"path"`
	Filename string `json:"filename"`
	MimeType string `json:"mime_type"`
}

func (c *weixinChannel) Handle(w http.ResponseWriter, r *http.Request, mgr *ChannelManager) {
	if r.Method != http.MethodPost {
		reject(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	secret, err := requireEnv(c.cfg.SharedSecretEnv)
	if err != nil {
		reject(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	// 外部网关用 Authorization: Bearer <UPSTREAM_SHARED_SECRET>，不是 HMAC。
	got := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if got == "" || !constEq(got, secret) {
		reject(w, http.StatusForbidden, "共享密钥校验失败")
		return
	}

	body, err := readBody(r, int64(mgr.app.cfg.Channels.MaxCallbackBytes))
	if err != nil {
		reject(w, http.StatusBadRequest, err.Error())
		return
	}
	var ev weixinEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		reject(w, http.StatusBadRequest, "非法事件体")
		return
	}
	if ev.Type != "message" {
		ackJSON(w, map[string]bool{"ok": true})
		return
	}

	text := strings.TrimSpace(ev.Text)
	if text == "" {
		// 附件落在「外部网关那台机器」的本地磁盘上（attachments[].path 是绝对路径），
		// 晨曦这边的 hostd 读不到。明确告诉用户，不假装处理了。
		if len(ev.Attachments) > 0 {
			text = "[收到附件] 晨曦当前只处理文本：附件保存在外部网关所在机器上，未转发到本机。"
		} else {
			ackJSON(w, map[string]bool{"ok": true})
			return
		}
	}
	if ev.AccountID == "" || ev.ContextToken == "" {
		reject(w, http.StatusBadRequest, "缺少 account_id / context_token，无法回复")
		return
	}
	to := ev.UserID
	if to == "" {
		to = ev.ChatID
	}
	chatType := ev.ChatType
	if chatType == "" {
		chatType = "c2c"
	}

	ok := mgr.Submit(inbound{
		eventID: ev.EventID, // gateway 已按 account_id + event_id 去重，这里再做一次
		text:    text,
		target:  to,
		ch:      c,
		extra: map[string]string{
			"account_id":    ev.AccountID,
			"to_user_id":    to,
			"context_token": ev.ContextToken,
			"chat_type":     chatType,
		},
	})
	if !ok {
		// 外部网关 README 只承诺「2xx 即可」，未承诺非 2xx 会重试。
		// 过载时这里可能丢消息，文档里如实写明；要可靠请改用 gateway 的 inbox 模式。
		log.Printf("微信网关：队列已满，返回 503（网关可能不重试）")
		w.Header().Set("Retry-After", "2")
		reject(w, http.StatusServiceUnavailable, "busy, retry")
		return
	}
	// callback 的职责只是接收事件；回复必须另外调用 /send。
	ackJSON(w, map[string]bool{"ok": true})
}

// Send 回复不走 HTTP 响应，而是回调外部网关的 /send。
func (c *weixinChannel) Send(ctx context.Context, _ string, text string, extra map[string]string) error {
	account := extra["account_id"]
	to := extra["to_user_id"]
	token := extra["context_token"]
	if account == "" || to == "" || token == "" {
		return fmt.Errorf("微信网关回复缺少 account_id / to_user_id / context_token")
	}
	chatType := extra["chat_type"]
	if chatType == "" {
		chatType = "c2c"
	}
	payload := map[string]interface{}{
		"account_id":    account,
		"to_user_id":    to,
		"context_token": token,
		"chat_type":     chatType,
		"items": []map[string]string{
			{"type": "text", "text": truncate(text, 4000)},
		},
	}
	u := strings.TrimRight(c.cfg.GatewayBase, "/") + c.cfg.SendPath
	_, err := c.mgr.doJSON(ctx, http.MethodPost, u, nil, payload, 8192)
	return err
}
