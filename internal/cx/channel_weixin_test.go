package cx

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestValidateWeixinGatewayRejectsUnsigned(t *testing.T) {
	if err := validateWeixinGateway(&WeixinGatewayConf{
		Path: "/x", GatewayBase: "http://127.0.0.1:8787",
	}); err == nil {
		t.Fatal("无共享密钥必须拒绝启动")
	}
	if err := validateWeixinGateway(&WeixinGatewayConf{
		Path: "/x", GatewayBase: "http://127.0.0.1:8787",
		SharedSecretEnv: "CHENXI_TEST_WX_MISSING",
	}); err == nil {
		t.Fatal("密钥环境变量未设置必须拒绝启动")
	}
	if err := validateWeixinGateway(&WeixinGatewayConf{
		Path: "/x", GatewayBase: "https://gw.example.com",
		SharedSecretEnv: "CHENXI_TEST_WX",
	}); err == nil {
		t.Fatal("示例地址必须拒绝启动")
	}
	t.Setenv("CHENXI_TEST_WX", "s3cr3t")
	if err := validateWeixinGateway(&WeixinGatewayConf{
		Path: "/x", GatewayBase: "http://192.168.1.50:8787",
		SharedSecretEnv: "CHENXI_TEST_WX",
	}); err != nil {
		t.Fatalf("配齐凭据应通过：%v", err)
	}
}

func TestWeixinGatewayHandleAndSend(t *testing.T) {
	t.Setenv("CHENXI_TEST_WX", "s3cr3t")
	var sent map[string]interface{}
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/send" {
			t.Errorf("回复路径应为 /send，实际 %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("/send 应为 POST")
		}
		_ = json.NewDecoder(r.Body).Decode(&sent)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer gw.Close()

	cfg := &Config{Channels: ChannelsConfig{MaxCallbackBytes: 65536}}
	cfg.Channels.WeixinGateway = WeixinGatewayConf{
		Path:            "/v1/channel/weixin",
		GatewayBase:     gw.URL,
		SendPath:        "/send",
		SharedSecretEnv: "CHENXI_TEST_WX",
	}
	mgr := &ChannelManager{
		app:     &App{cfg: cfg},
		routes:  make(map[string]Channel),
		seen:    make(map[string]int64),
		maxSeen: 16,
		queue:   make(chan inbound, 4),
		client:  gw.Client(),
		timeout: 0,
	}
	ch := newWeixinChannel(mgr, &cfg.Channels.WeixinGateway)

	event := `{"type":"message","account_id":"wx-account-1","event_id":"evt-1",` +
		`"chat_id":"wx-user-1","user_id":"wx-user-1","text":"你好晨曦",` +
		`"context_token":"ctx-1","chat_type":"c2c","raw":{}}`

	// 1. 无 Bearer 必须 403
	req := httptest.NewRequest(http.MethodPost, "/v1/channel/weixin", strings.NewReader(event))
	w := httptest.NewRecorder()
	ch.Handle(w, req, mgr)
	if w.Code != http.StatusForbidden {
		t.Fatalf("无鉴权应 403，实际 %d", w.Code)
	}

	// 2. 错误密钥必须 403
	req = httptest.NewRequest(http.MethodPost, "/v1/channel/weixin", strings.NewReader(event))
	req.Header.Set("Authorization", "Bearer wrong")
	w = httptest.NewRecorder()
	ch.Handle(w, req, mgr)
	if w.Code != http.StatusForbidden {
		t.Fatalf("错误密钥应 403，实际 %d", w.Code)
	}

	// 3. 合法事件 → 立即 ACK，进队列
	req = httptest.NewRequest(http.MethodPost, "/v1/channel/weixin", strings.NewReader(event))
	req.Header.Set("Authorization", "Bearer s3cr3t")
	w = httptest.NewRecorder()
	ch.Handle(w, req, mgr)
	if w.Code != http.StatusOK {
		t.Fatalf("合法事件应 200，实际 %d：%s", w.Code, w.Body.String())
	}
	if len(mgr.queue) != 1 {
		t.Fatalf("事件未入队，队列长度 %d", len(mgr.queue))
	}
	task := <-mgr.queue
	if task.eventID != "evt-1" || task.text != "你好晨曦" {
		t.Fatalf("事件归一化错误：%+v", task)
	}
	for k, want := range map[string]string{
		"account_id": "wx-account-1", "to_user_id": "wx-user-1",
		"context_token": "ctx-1", "chat_type": "c2c",
	} {
		if task.extra[k] != want {
			t.Fatalf("extra[%s]=%q，期望 %q", k, task.extra[k], want)
		}
	}

	// 4. 重复 event_id 静默 ACK，不重复入队
	req = httptest.NewRequest(http.MethodPost, "/v1/channel/weixin", strings.NewReader(event))
	req.Header.Set("Authorization", "Bearer s3cr3t")
	w = httptest.NewRecorder()
	ch.Handle(w, req, mgr)
	if w.Code != http.StatusOK || len(mgr.queue) != 0 {
		t.Fatalf("重复事件应静默 ACK 且不入队：code=%d queue=%d", w.Code, len(mgr.queue))
	}

	// 5. Send 必须按 /send schema 带 context_token
	if err := ch.Send(context.Background(), "", "你好，我收到了。", task.extra); err != nil {
		t.Fatalf("/send 调用失败：%v", err)
	}
	if sent["account_id"] != "wx-account-1" || sent["to_user_id"] != "wx-user-1" ||
		sent["context_token"] != "ctx-1" || sent["chat_type"] != "c2c" {
		t.Fatalf("/send 字段不符：%+v", sent)
	}
	items, ok := sent["items"].([]interface{})
	if !ok || len(items) != 1 {
		t.Fatalf("/send items 结构错误：%+v", sent["items"])
	}
	item := items[0].(map[string]interface{})
	if item["type"] != "text" || item["text"] != "你好，我收到了。" {
		t.Fatalf("/send 文本项不符：%+v", item)
	}
}

func TestWeixinGatewayAttachmentOnlyMessage(t *testing.T) {
	t.Setenv("CHENXI_TEST_WX", "s3cr3t")
	cfg := &Config{Channels: ChannelsConfig{MaxCallbackBytes: 65536}}
	cfg.Channels.WeixinGateway = WeixinGatewayConf{
		Path: "/x", GatewayBase: "http://127.0.0.1:8787",
		SendPath: "/send", SharedSecretEnv: "CHENXI_TEST_WX",
	}
	mgr := &ChannelManager{
		app: &App{cfg: cfg}, routes: make(map[string]Channel),
		seen: make(map[string]int64), maxSeen: 16, queue: make(chan inbound, 4),
	}
	ch := newWeixinChannel(mgr, &cfg.Channels.WeixinGateway)
	event := `{"type":"message","account_id":"wx-1","event_id":"evt-2",` +
		`"user_id":"u1","text":"","context_token":"ctx-2","chat_type":"c2c",` +
		`"attachments":[{"kind":"image","path":"/Users/you/.data/inbound/a.png",` +
		`"filename":"a.png","mime_type":"image/png"}]}`
	req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(event))
	req.Header.Set("Authorization", "Bearer s3cr3t")
	w := httptest.NewRecorder()
	ch.Handle(w, req, mgr)
	if w.Code != http.StatusOK || len(mgr.queue) != 1 {
		t.Fatalf("附件消息应被受理并给出说明：code=%d queue=%d", w.Code, len(mgr.queue))
	}
	task := <-mgr.queue
	if !strings.Contains(task.text, "附件保存在外部网关") {
		t.Fatalf("未如实说明附件不可读：%q", task.text)
	}
}

func TestWeixinGatewayRejectsIncompleteEvent(t *testing.T) {
	t.Setenv("CHENXI_TEST_WX", "s3cr3t")
	cfg := &Config{Channels: ChannelsConfig{MaxCallbackBytes: 65536}}
	cfg.Channels.WeixinGateway = WeixinGatewayConf{
		Path: "/x", GatewayBase: "http://127.0.0.1:8787",
		SendPath: "/send", SharedSecretEnv: "CHENXI_TEST_WX",
	}
	mgr := &ChannelManager{
		app: &App{cfg: cfg}, routes: make(map[string]Channel),
		seen: make(map[string]int64), maxSeen: 16, queue: make(chan inbound, 4),
	}
	ch := newWeixinChannel(mgr, &cfg.Channels.WeixinGateway)
	// 缺 context_token：无法回复，必须拒绝，不能静默丢进队列。
	event := `{"type":"message","account_id":"wx-1","event_id":"evt-3",` +
		`"user_id":"u1","text":"你好","chat_type":"c2c"}`
	req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(event))
	req.Header.Set("Authorization", "Bearer s3cr3t")
	w := httptest.NewRecorder()
	ch.Handle(w, req, mgr)
	if w.Code != http.StatusBadRequest || len(mgr.queue) != 0 {
		t.Fatalf("缺 context_token 应 400 且不入队：code=%d queue=%d", w.Code, len(mgr.queue))
	}
}
