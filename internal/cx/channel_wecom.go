package cx

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type wecomChannel struct {
	mgr   *ChannelManager
	cfg   *WeComConf
	token *tokenCache
}

func validateWeCom(c *WeComConf) error {
	if c.Path == "" {
		return fmt.Errorf("wecom.path 未配置")
	}
	if c.TokenEnv == "" || c.AESKeyEnv == "" {
		return fmt.Errorf("企业微信渠道必须配置 token_env / aes_key_env，拒绝无验签回调")
	}
	if c.CorpID == "" || c.SecretEnv == "" || c.AgentID == "" {
		return fmt.Errorf("企业微信渠道缺少 corp_id / agent_id / secret_env")
	}
	for _, k := range []string{c.TokenEnv, c.AESKeyEnv, c.SecretEnv} {
		if getenv(k) == "" {
			return fmt.Errorf("企业微信凭据环境变量 %s 未设置", k)
		}
	}
	return nil
}

func newWeComChannel(m *ChannelManager, c *WeComConf) Channel {
	ch := &wecomChannel{mgr: m, cfg: c}
	ch.token = &tokenCache{fetch: ch.fetchToken}
	return ch
}

func (c *wecomChannel) Name() string { return "wecom" }

type wecomEnvelope struct {
	XMLName    xml.Name `xml:"xml"`
	ToUserName string   `xml:"ToUserName"`
	AgentID    string   `xml:"AgentID"`
	Encrypt    string   `xml:"Encrypt"`
}

type wecomMessage struct {
	XMLName      xml.Name `xml:"xml"`
	ToUserName   string   `xml:"ToUserName"`
	FromUserName string   `xml:"FromUserName"`
	CreateTime   string   `xml:"CreateTime"`
	MsgType      string   `xml:"MsgType"`
	Content      string   `xml:"Content"`
	MsgID        string   `xml:"MsgId"`
	Event        string   `xml:"Event"`
	AgentID      string   `xml:"AgentID"`
}

func (c *wecomChannel) creds() (token, aesKey string, err error) {
	token, err = requireEnv(c.cfg.TokenEnv)
	if err != nil {
		return
	}
	aesKey, err = requireEnv(c.cfg.AESKeyEnv)
	return
}

func (c *wecomChannel) Handle(w http.ResponseWriter, r *http.Request, mgr *ChannelManager) {
	q := r.URL.Query()
	token, aesKey, err := c.creds()
	if err != nil {
		reject(w, http.StatusServiceUnavailable, err.Error())
		return
	}

	if r.Method == http.MethodGet {
		// 后台保存回调配置时的 URL 验证：验签 → 解密 echostr → 原样回显明文。
		echo := q.Get("echostr")
		want := wecomSignature(token, q.Get("timestamp"), q.Get("nonce"), echo)
		if !constEq(want, q.Get("msg_signature")) {
			reject(w, http.StatusForbidden, "签名校验失败")
			return
		}
		plain, err := decryptWecom(aesKey, echo, c.cfg.CorpID)
		if err != nil {
			reject(w, http.StatusForbidden, "echostr 解密失败")
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		// 官方要求：不加引号、不带 BOM、不带换行，原样返回。
		_, _ = w.Write(plain)
		return
	}
	if r.Method != http.MethodPost {
		reject(w, http.StatusMethodNotAllowed, "GET/POST only")
		return
	}

	body, err := readBody(r, int64(mgr.app.cfg.Channels.MaxCallbackBytes))
	if err != nil {
		reject(w, http.StatusBadRequest, err.Error())
		return
	}
	var env wecomEnvelope
	if xml.Unmarshal(body, &env) != nil || env.Encrypt == "" {
		var j struct {
			Encrypt string `json:"encrypt"`
		}
		if json.Unmarshal(body, &j) == nil {
			env.Encrypt = j.Encrypt
		}
	}
	if env.Encrypt == "" {
		reject(w, http.StatusBadRequest, "缺少 Encrypt 字段")
		return
	}
	want := wecomSignature(token, q.Get("timestamp"), q.Get("nonce"), env.Encrypt)
	if !constEq(want, q.Get("msg_signature")) {
		reject(w, http.StatusForbidden, "签名校验失败")
		return
	}
	msgBytes, err := decryptWecom(aesKey, env.Encrypt, c.cfg.CorpID)
	if err != nil {
		reject(w, http.StatusForbidden, "消息解密失败")
		return
	}
	var msg wecomMessage
	if err := xml.Unmarshal(msgBytes, &msg); err != nil {
		reject(w, http.StatusBadRequest, "消息 XML 解析失败")
		return
	}
	if msg.MsgType != "text" || strings.TrimSpace(msg.Content) == "" {
		// 空包 200：企微只认 200，非 200 会重试 3 次。
		ackOK(w)
		return
	}

	eventID := msg.MsgID
	if eventID == "" {
		eventID = msg.FromUserName + "|" + msg.CreateTime + "|" + msg.Event
	}
	ok := mgr.Submit(inbound{
		eventID: eventID,
		text:    msg.Content,
		target:  msg.FromUserName,
		ch:      c,
		extra:   map[string]string{"agent_id": msg.AgentID},
	})
	if !ok {
		log.Printf("企微：队列已满，返回 503 触发平台重试")
		w.Header().Set("Retry-After", "2")
		reject(w, http.StatusServiceUnavailable, "busy, retry")
		return
	}
	// 5 秒内先 ACK，回复走主动发消息接口异步送达。
	ackOK(w)
}

func (c *wecomChannel) fetchToken(ctx context.Context) (string, int, error) {
	secret, err := requireEnv(c.cfg.SecretEnv)
	if err != nil {
		return "", 0, err
	}
	u := strings.TrimRight(c.cfg.APIBase, "/") + "/cgi-bin/gettoken?corpid=" +
		url.QueryEscape(c.cfg.CorpID) + "&corpsecret=" + url.QueryEscape(secret)
	data, err := c.mgr.doJSON(ctx, http.MethodGet, u, nil, nil, 8192)
	if err != nil {
		return "", 0, err
	}
	var out struct {
		ErrCode     int    `json:"errcode"`
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", 0, err
	}
	if out.ErrCode != 0 || out.AccessToken == "" {
		return "", 0, fmt.Errorf("企微取 access_token 失败 errcode=%d", out.ErrCode)
	}
	return out.AccessToken, out.ExpiresIn, nil
}

func (c *wecomChannel) Send(ctx context.Context, target, text string, _ map[string]string) error {
	if target == "" {
		target = c.cfg.DefaultTarget
	}
	if target == "" {
		target = "@all"
	}
	token, err := c.token.Get(ctx)
	if err != nil {
		return err
	}
	agentID, err := strconv.Atoi(c.cfg.AgentID)
	if err != nil {
		return fmt.Errorf("wecom.agent_id 不是整数：%w", err)
	}
	payload := map[string]interface{}{
		"touser":  target,
		"msgtype": "text",
		"agentid": agentID,
		"text":    map[string]string{"content": truncate(text, 20000)},
	}
	u := strings.TrimRight(c.cfg.APIBase, "/") + "/cgi-bin/message/send?access_token=" + url.QueryEscape(token)
	data, err := c.mgr.doJSON(ctx, http.MethodPost, u, nil, payload, 8192)
	if err != nil {
		return err
	}
	var out struct {
		ErrCode int `json:"errcode"`
	}
	_ = json.Unmarshal(data, &out)
	if out.ErrCode != 0 {
		return fmt.Errorf("企微发消息失败 errcode=%d", out.ErrCode)
	}
	return nil
}
