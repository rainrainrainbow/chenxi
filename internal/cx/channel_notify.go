package cx

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// 单向通知出口：把晨报、告警、通用 Webhook 的回复推到微信（服务号推送 / 群机器人）。
// 个人微信没有官方 bot API，这里只做「发得出去」，不做「收得进来」。
func notifySend(m *ChannelManager, ctx context.Context, title, text string) error {
	cfg := &m.app.cfg.Channels.Notify
	if !cfg.Enabled {
		return fmt.Errorf("通知出口未启用（channels.notify.enabled=false）")
	}
	u, err := requireEnv(cfg.URLEnv)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
		return fmt.Errorf("通知 URL 必须是 http/https")
	}
	if cfg.Title != "" {
		title = cfg.Title
	}
	content := title + "\n\n" + text
	if len(content) > 20000 {
		content = truncate(content, 20000)
	}

	var ct string
	var payload []byte
	switch cfg.Kind {
	case "feishu_bot":
		ct = "application/json; charset=utf-8"
		payload, _ = json.Marshal(map[string]interface{}{
			"msg_type": "text",
			"content":  map[string]string{"text": content},
		})
	case "wecom_bot":
		ct = "application/json; charset=utf-8"
		payload, _ = json.Marshal(map[string]interface{}{
			"msgtype": "text",
			"text":    map[string]string{"content": content},
		})
	case "bark":
		ct = "application/json; charset=utf-8"
		payload, _ = json.Marshal(map[string]string{"title": title, "body": text})
	case "pushplus":
		ct = "application/json; charset=utf-8"
		payload, _ = json.Marshal(map[string]string{
			"token":   getenv("CHENXI_NOTIFY_TOKEN"),
			"title":   title,
			"content": text,
		})
	case "serverchan":
		ct = "application/x-www-form-urlencoded"
		v := url.Values{"title": {title}, "desp": {text}}
		payload = []byte(v.Encode())
	case "custom", "":
		ct = "application/json; charset=utf-8"
		payload, _ = json.Marshal(map[string]string{"title": title, "text": text})
	default:
		return fmt.Errorf("未知 notify.kind %q", cfg.Kind)
	}
	return m.postRaw(ctx, u, ct, payload)
}

func (m *ChannelManager) postRaw(ctx context.Context, u, ct string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", ct)
	resp, err := m.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8193))
	if err != nil {
		return err
	}
	if len(data) > 8192 {
		return fmt.Errorf("通知平台响应超过预算")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("通知平台 HTTP %d：%s", resp.StatusCode, truncate(string(data), 200))
	}
	return nil
}
