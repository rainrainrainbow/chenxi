package cx

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
)

// 通用 Webhook：任何能发 HTTP 回调的系统（n8n、Home Assistant、Telegram 桥、
// 自建前端、下一位平台）都能接。必须带 HMAC-SHA256 签名，拒绝裸奔。
type webhookChannel struct {
	mgr *ChannelManager
	cfg *WebhookConf
}

func validateWebhook(c *WebhookConf) error {
	if c.Path == "" {
		return fmt.Errorf("webhook.path 未配置")
	}
	if c.SecretEnv == "" {
		return fmt.Errorf("通用 Webhook 必须配置 secret_env，拒绝无签名回调")
	}
	if getenv(c.SecretEnv) == "" {
		return fmt.Errorf("通用 Webhook 的环境变量 %s 未设置", c.SecretEnv)
	}
	return nil
}

func newWebhookChannel(m *ChannelManager, c *WebhookConf) Channel {
	return &webhookChannel{mgr: m, cfg: c}
}

func (c *webhookChannel) Name() string { return "webhook" }

type webhookIn struct {
	EventID  string `json:"event_id"`
	Text     string `json:"text"`
	Target   string `json:"target"`
	ReplyURL string `json:"reply_url"`
}

func (c *webhookChannel) Handle(w http.ResponseWriter, r *http.Request, mgr *ChannelManager) {
	if r.Method != http.MethodPost {
		reject(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	body, err := readBody(r, int64(mgr.app.cfg.Channels.MaxCallbackBytes))
	if err != nil {
		reject(w, http.StatusBadRequest, err.Error())
		return
	}
	secret, err := requireEnv(c.cfg.SecretEnv)
	if err != nil {
		reject(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	got := strings.TrimPrefix(r.Header.Get("X-Chenxi-Signature"), "sha256=")
	if !constEq(hmacSHA256Hex(secret, body), got) {
		reject(w, http.StatusForbidden, "HMAC 签名校验失败")
		return
	}
	var in webhookIn
	if err := json.Unmarshal(body, &in); err != nil || strings.TrimSpace(in.Text) == "" {
		ackJSON(w, map[string]string{"status": "ignored"})
		return
	}
	ok := mgr.Submit(inbound{
		eventID: in.EventID,
		text:    in.Text,
		target:  in.Target,
		ch:      c,
		extra:   map[string]string{"reply_url": in.ReplyURL},
	})
	if !ok {
		log.Printf("通用 Webhook：队列已满，返回 503")
		w.Header().Set("Retry-After", "2")
		reject(w, http.StatusServiceUnavailable, "busy, retry")
		return
	}
	ackJSON(w, map[string]string{"status": "accepted"})
}

func (c *webhookChannel) Send(ctx context.Context, target, text string, extra map[string]string) error {
	if u := extra["reply_url"]; u != "" {
		if !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
			return fmt.Errorf("reply_url 协议非法")
		}
		_, err := c.mgr.doJSON(ctx, http.MethodPost, u, nil,
			map[string]string{"target": target, "text": text}, 8192)
		return err
	}
	return c.mgr.Notify(ctx, "晨曦", text)
}
