package cx

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
)

type qqChannel struct {
	mgr   *ChannelManager
	cfg   *QQConf
	pub   ed25519.PublicKey
	priv  ed25519.PrivateKey
	token *tokenCache
}

func validateQQ(c *QQConf) error {
	if c.Path == "" {
		return fmt.Errorf("qq.path 未配置")
	}
	if c.AppIDEnv == "" || c.SecretEnv == "" {
		return fmt.Errorf("QQ 渠道必须配置 app_id_env / secret_env（Bot Secret 同时用于 Ed25519 验签）")
	}
	if getenv(c.AppIDEnv) == "" || getenv(c.SecretEnv) == "" {
		return fmt.Errorf("QQ 渠道凭据环境变量未设置（%s / %s）", c.AppIDEnv, c.SecretEnv)
	}
	return nil
}

func newQQChannel(m *ChannelManager, c *QQConf) Channel {
	secret := getenv(c.SecretEnv)
	pub, priv := qqKeys(secret)
	ch := &qqChannel{mgr: m, cfg: c, pub: pub, priv: priv}
	ch.token = &tokenCache{fetch: ch.fetchToken}
	return ch
}

func (c *qqChannel) Name() string { return "qq" }

type qqPayload struct {
	Op int             `json:"op"`
	T  string          `json:"t"`
	ID string          `json:"id"`
	D  json.RawMessage `json:"d"`
}

type qqValidation struct {
	PlainToken string `json:"plain_token"`
	EventTS    string `json:"event_ts"`
}

type qqEvent struct {
	ID          string `json:"id"`
	Content     string `json:"content"`
	Timestamp   string `json:"timestamp"`
	GroupOpenID string `json:"group_openid"`
	Author      struct {
		UserOpenID string `json:"user_openid"`
	} `json:"author"`
}

func (c *qqChannel) Handle(w http.ResponseWriter, r *http.Request, mgr *ChannelManager) {
	if r.Method != http.MethodPost {
		reject(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	// 验签基于原始 body：反向代理不得改写/重新序列化请求体。
	body, err := readBody(r, int64(mgr.app.cfg.Channels.MaxCallbackBytes))
	if err != nil {
		reject(w, http.StatusBadRequest, err.Error())
		return
	}
	sig := r.Header.Get("X-Signature-Ed25519")
	ts := r.Header.Get("X-Signature-Timestamp")
	// 有签名头就必须验通过；平台配置阶段的握手（op=13）可能不带签名头，
	// 但它靠「能用 Bot Secret 签回 plain_token」自证身份，可放行。
	// 真正的事件（op=0）无论什么情况都必须验签。
	verified := sig != "" && qqVerify(c.pub, sig, ts, body)
	if sig != "" && !verified {
		reject(w, http.StatusForbidden, "Ed25519 签名校验失败")
		return
	}
	var p qqPayload
	if err := json.Unmarshal(body, &p); err != nil {
		reject(w, http.StatusBadRequest, "非法 payload")
		return
	}
	if p.Op == 0 && !verified {
		reject(w, http.StatusForbidden, "事件回调缺少有效 Ed25519 签名")
		return
	}

	// op=13：回调地址验证，用私钥签 event_ts + plain_token。
	if p.Op == 13 {
		var v qqValidation
		if err := json.Unmarshal(p.D, &v); err != nil || v.PlainToken == "" {
			reject(w, http.StatusBadRequest, "非法验证请求")
			return
		}
		ackJSON(w, map[string]string{
			"plain_token": v.PlainToken,
			"signature":   qqSign(c.priv, v.EventTS, v.PlainToken),
		})
		return
	}
	if p.Op != 0 {
		// 心跳、重连等非事件 op 直接 ACK。
		ackOK(w)
		return
	}
	switch p.T {
	case "C2C_MESSAGE_CREATE", "GROUP_AT_MESSAGE_CREATE", "AT_MESSAGE_CREATE", "MESSAGE_CREATE":
	default:
		ackOK(w)
		return
	}
	var ev qqEvent
	if err := json.Unmarshal(p.D, &ev); err != nil {
		ackOK(w)
		return
	}
	text := stripMentions(ev.Content)
	if text == "" {
		ackOK(w)
		return
	}
	target := "user:" + ev.Author.UserOpenID
	if ev.GroupOpenID != "" {
		target = "group:" + ev.GroupOpenID
	}
	eventID := p.ID
	if eventID == "" {
		eventID = ev.ID
	}
	ok := mgr.Submit(inbound{
		eventID: eventID,
		text:    text,
		target:  target,
		ch:      c,
		extra:   map[string]string{"msg_id": ev.ID},
	})
	if !ok {
		log.Printf("QQ：队列已满，返回 503 触发平台重试")
		w.Header().Set("Retry-After", "2")
		reject(w, http.StatusServiceUnavailable, "busy, retry")
		return
	}
	ackOK(w)
}

func (c *qqChannel) fetchToken(ctx context.Context) (string, int, error) {
	appID, err := requireEnv(c.cfg.AppIDEnv)
	if err != nil {
		return "", 0, err
	}
	secret, err := requireEnv(c.cfg.SecretEnv)
	if err != nil {
		return "", 0, err
	}
	u := strings.TrimRight(c.cfg.APIBase, "/") + "/app/getAppAccessToken"
	data, err := c.mgr.doJSON(ctx, http.MethodPost, u, nil,
		map[string]string{"appId": appID, "clientSecret": secret}, 8192)
	if err != nil {
		return "", 0, err
	}
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		Code        int    `json:"code"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", 0, err
	}
	if out.AccessToken == "" {
		return "", 0, fmt.Errorf("QQ 取 access_token 失败 code=%d", out.Code)
	}
	return out.AccessToken, out.ExpiresIn, nil
}

func (c *qqChannel) Send(ctx context.Context, target, text string, extra map[string]string) error {
	token, err := c.token.Get(ctx)
	if err != nil {
		return err
	}
	kind, id := "users", ""
	if strings.HasPrefix(target, "user:") {
		id = strings.TrimPrefix(target, "user:")
	} else if strings.HasPrefix(target, "group:") {
		kind, id = "groups", strings.TrimPrefix(target, "group:")
	} else {
		kind, id = "users", target
	}
	if id == "" {
		return fmt.Errorf("QQ 回复目标为空")
	}
	payload := map[string]interface{}{
		"content":  truncate(text, 5000),
		"msg_type": 0,
	}
	// msg_id 为被动回复凭证（5 分钟内有效）；异步处理通常远小于该窗口。
	if mid := extra["msg_id"]; mid != "" {
		payload["msg_id"] = mid
	}
	u := strings.TrimRight(c.cfg.APIBase, "/") + "/v2/" + kind + "/" + id + "/messages"
	data, err := c.mgr.doJSON(ctx, http.MethodPost, u,
		map[string]string{"Authorization": "QQBot " + token}, payload, 8192)
	if err != nil {
		return err
	}
	var out struct {
		ErrCode int `json:"err_code"`
	}
	_ = json.Unmarshal(data, &out)
	if out.ErrCode != 0 {
		return fmt.Errorf("QQ 发消息失败 err_code=%d", out.ErrCode)
	}
	return nil
}
