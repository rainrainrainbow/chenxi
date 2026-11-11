package cx

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strings"
)

var mentionRE = regexp.MustCompile(`@_user_[0-9]+`)

func stripMentions(s string) string {
	return strings.TrimSpace(mentionRE.ReplaceAllString(s, ""))
}

type feishuChannel struct {
	mgr   *ChannelManager
	cfg   *FeishuConf
	token *tokenCache
}

func validateFeishu(c *FeishuConf) error {
	if c.Path == "" {
		return fmt.Errorf("feishu.path 未配置")
	}
	if c.AppIDEnv == "" || c.AppSecretEnv == "" {
		return fmt.Errorf("飞书渠道必须在 config.json 指定 app_id_env / app_secret_env（值走环境变量）")
	}
	if c.EncryptKeyEnv == "" && c.VerifyTokenEnv == "" {
		return fmt.Errorf("飞书渠道拒绝裸奔回调：请配置 Encrypt Key 或 Verification Token 的环境变量名")
	}
	if getenv(c.AppIDEnv) == "" || getenv(c.AppSecretEnv) == "" {
		return fmt.Errorf("飞书渠道凭据环境变量未设置（%s / %s）", c.AppIDEnv, c.AppSecretEnv)
	}
	if c.EncryptKeyEnv != "" && getenv(c.EncryptKeyEnv) == "" {
		return fmt.Errorf("飞书 Encrypt Key 环境变量 %s 未设置", c.EncryptKeyEnv)
	}
	if c.VerifyTokenEnv != "" && getenv(c.VerifyTokenEnv) == "" {
		return fmt.Errorf("飞书 Verification Token 环境变量 %s 未设置", c.VerifyTokenEnv)
	}
	return nil
}

func newFeishuChannel(m *ChannelManager, c *FeishuConf) Channel {
	ch := &feishuChannel{mgr: m, cfg: c}
	ch.token = &tokenCache{fetch: ch.fetchToken}
	return ch
}

func (c *feishuChannel) Name() string { return "feishu" }

type feishuEnvelope struct {
	Type      string `json:"type"`
	Challenge string `json:"challenge"`
	Token     string `json:"token"`
	Schema    string `json:"schema"`
	Encrypt   string `json:"encrypt"`
	Header    struct {
		EventID   string `json:"event_id"`
		EventType string `json:"event_type"`
		Token     string `json:"token"`
	} `json:"header"`
	Event struct {
		Message struct {
			MessageID   string `json:"message_id"`
			ChatID      string `json:"chat_id"`
			ChatType    string `json:"chat_type"`
			MessageType string `json:"message_type"`
			Content     string `json:"content"`
		} `json:"message"`
	} `json:"event"`
}

func (c *feishuChannel) Handle(w http.ResponseWriter, r *http.Request, mgr *ChannelManager) {
	if r.Method != http.MethodPost {
		reject(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	body, err := readBody(r, int64(mgr.app.cfg.Channels.MaxCallbackBytes))
	if err != nil {
		reject(w, http.StatusBadRequest, err.Error())
		return
	}
	encrypted := false

	if c.cfg.EncryptKeyEnv != "" {
		var box struct {
			Encrypt string `json:"encrypt"`
		}
		if json.Unmarshal(body, &box) == nil && box.Encrypt != "" {
			key, err := requireEnv(c.cfg.EncryptKeyEnv)
			if err != nil {
				reject(w, http.StatusServiceUnavailable, err.Error())
				return
			}
			body, err = decryptFeishu(key, box.Encrypt)
			if err != nil {
				reject(w, http.StatusForbidden, "飞书事件解密失败")
				return
			}
			encrypted = true
		}
	}

	var env feishuEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		reject(w, http.StatusBadRequest, "非法事件体")
		return
	}

	if c.cfg.VerifyTokenEnv != "" {
		want, err := requireEnv(c.cfg.VerifyTokenEnv)
		if err != nil {
			reject(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		got := env.Token
		if got == "" {
			got = env.Header.Token
		}
		if !constEq(got, want) {
			reject(w, http.StatusForbidden, "Verification Token 不匹配")
			return
		}
	}

	// challenge 必须 1 秒内原样返回，不能进 Agent Loop。
	if env.Type == "url_verification" || env.Challenge != "" {
		ackJSON(w, map[string]string{"challenge": env.Challenge})
		return
	}
	// 事件必须至少有一种来源校验：加密推送，或 Verification Token。
	// challenge 豁免是因为它只回显、不产生副作用，且平台可能在配置加密前发。
	if !encrypted && c.cfg.VerifyTokenEnv == "" {
		reject(w, http.StatusForbidden, "飞书事件必须启用 Encrypt Key 或 Verification Token")
		return
	}
	if env.Header.EventType != "im.message.receive_v1" || env.Event.Message.MessageType != "text" {
		ackJSON(w, map[string]int{"code": 0})
		return
	}
	var content struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(env.Event.Message.Content), &content); err != nil {
		ackJSON(w, map[string]int{"code": 0})
		return
	}
	text := stripMentions(content.Text)
	if text == "" {
		ackJSON(w, map[string]int{"code": 0})
		return
	}

	ok := mgr.Submit(inbound{
		eventID: env.Header.EventID,
		text:    text,
		target:  env.Event.Message.ChatID,
		ch:      c,
		extra:   map[string]string{"message_id": env.Event.Message.MessageID},
	})
	if !ok {
		log.Printf("飞书：队列已满，返回 503 让平台重试")
		w.Header().Set("Retry-After", "2")
		reject(w, http.StatusServiceUnavailable, "busy, retry")
		return
	}
	ackJSON(w, map[string]int{"code": 0})
}

func (c *feishuChannel) fetchToken(ctx context.Context) (string, int, error) {
	appID, err := requireEnv(c.cfg.AppIDEnv)
	if err != nil {
		return "", 0, err
	}
	secret, err := requireEnv(c.cfg.AppSecretEnv)
	if err != nil {
		return "", 0, err
	}
	url := strings.TrimRight(c.cfg.APIBase, "/") + "/open-apis/auth/v3/tenant_access_token/internal"
	data, err := c.mgr.doJSON(ctx, http.MethodPost, url, nil,
		map[string]string{"app_id": appID, "app_secret": secret}, 8192)
	if err != nil {
		return "", 0, err
	}
	var out struct {
		Code              int    `json:"code"`
		TenantAccessToken string `json:"tenant_access_token"`
		Expire            int    `json:"expire"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", 0, err
	}
	if out.Code != 0 || out.TenantAccessToken == "" {
		return "", 0, fmt.Errorf("飞书取 tenant_access_token 失败 code=%d", out.Code)
	}
	return out.TenantAccessToken, out.Expire, nil
}

func (c *feishuChannel) Send(ctx context.Context, target, text string, extra map[string]string) error {
	if target == "" {
		target = c.cfg.DefaultTarget
	}
	if target == "" {
		return fmt.Errorf("飞书回复目标为空（chat_id）")
	}
	token, err := c.token.Get(ctx)
	if err != nil {
		return err
	}
	contentBytes, _ := json.Marshal(map[string]string{"text": truncate(text, 20000)})
	payload := map[string]string{
		"receive_id": target,
		"msg_type":   "text",
		"content":    string(contentBytes),
	}
	if id := extra["message_id"]; id != "" {
		// 飞书相同 uuid 1 小时内至多成功发送一条；message_id 可能超 50 字符，先截断。
		payload["uuid"] = truncate(id, 50)
	}
	url := strings.TrimRight(c.cfg.APIBase, "/") + "/open-apis/im/v1/messages?receive_id_type=chat_id"
	_, err = c.mgr.doJSON(ctx, http.MethodPost, url,
		map[string]string{"Authorization": "Bearer " + token}, payload, 8192)
	return err
}
