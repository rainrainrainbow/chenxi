package cx

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sync"
	"time"
)

func getenv(k string) string { return os.Getenv(k) }

func envOr(k, def string) string {
	if v := getenv(k); v != "" {
		return v
	}
	return def
}

// Channel 只做三件事：验签/握手、提取文本、回复。
// 任何渠道都不得绕开 ChannelManager.Submit → App.Handle 这条唯一入口。
type Channel interface {
	Name() string
	Handle(w http.ResponseWriter, r *http.Request, mgr *ChannelManager)
	Send(ctx context.Context, target, text string, extra map[string]string) error
}

type inbound struct {
	eventID string
	text    string
	target  string
	ch      Channel
	extra   map[string]string
}

type ChannelManager struct {
	app     *App
	routes  map[string]Channel
	mu      sync.Mutex
	seen    map[string]int64
	order   []string
	maxSeen int
	queue   chan inbound
	client  *http.Client
	timeout time.Duration
}

func NewChannelManager(app *App) (*ChannelManager, error) {
	cfg := &app.cfg.Channels
	if cfg.ReplyTimeoutSec <= 0 {
		cfg.ReplyTimeoutSec = 60
	}
	if cfg.MaxCallbackBytes <= 0 {
		cfg.MaxCallbackBytes = 65536
	}
	if app.cfg.Budgets.MaxConcurrency < 1 {
		app.cfg.Budgets.MaxConcurrency = 1
	}
	m := &ChannelManager{
		app:     app,
		routes:  make(map[string]Channel),
		seen:    make(map[string]int64),
		maxSeen: cfg.MaxDedup,
		queue:   make(chan inbound, app.cfg.Budgets.MaxConcurrency*2),
		client:  &http.Client{Timeout: time.Duration(cfg.ReplyTimeoutSec) * time.Second},
		timeout: time.Duration(cfg.ReplyTimeoutSec) * time.Second,
	}
	if m.maxSeen <= 0 {
		m.maxSeen = 256
	}

	if cfg.Feishu.Enabled {
		if err := validateFeishu(&cfg.Feishu); err != nil {
			return nil, err
		}
		m.register(cfg.Feishu.Path, newFeishuChannel(m, &cfg.Feishu))
	}
	if cfg.WeCom.Enabled {
		if err := validateWeCom(&cfg.WeCom); err != nil {
			return nil, err
		}
		m.register(cfg.WeCom.Path, newWeComChannel(m, &cfg.WeCom))
	}
	if cfg.QQ.Enabled {
		if err := validateQQ(&cfg.QQ); err != nil {
			return nil, err
		}
		m.register(cfg.QQ.Path, newQQChannel(m, &cfg.QQ))
	}
	if cfg.Webhook.Enabled {
		if err := validateWebhook(&cfg.Webhook); err != nil {
			return nil, err
		}
		m.register(cfg.Webhook.Path, newWebhookChannel(m, &cfg.Webhook))
	}
	if cfg.WeixinGateway.Enabled {
		if err := validateWeixinGateway(&cfg.WeixinGateway); err != nil {
			return nil, err
		}
		m.register(cfg.WeixinGateway.Path, newWeixinChannel(m, &cfg.WeixinGateway))
	}

	if len(m.routes) > 0 {
		for i := 0; i < app.cfg.Budgets.MaxConcurrency; i++ {
			go m.worker()
		}
	}
	return m, nil
}

func (m *ChannelManager) register(path string, ch Channel) {
	if path == "" {
		return
	}
	if _, dup := m.routes[path]; dup {
		log.Printf("渠道路径冲突，跳过 %s：%s", path, ch.Name())
		return
	}
	m.routes[path] = ch
	log.Printf("渠道已启用：%s → %s", ch.Name(), path)
}

func (m *ChannelManager) ServeRoutes(mux *http.ServeMux) {
	for path, ch := range m.routes {
		p, c := path, ch
		mux.HandleFunc(p, func(w http.ResponseWriter, r *http.Request) {
			c.Handle(w, r, m)
		})
	}
}

func (m *ChannelManager) Enabled() []string {
	out := make([]string, 0, len(m.routes))
	for _, ch := range m.routes {
		out = append(out, ch.Name())
	}
	return out
}

func (m *ChannelManager) QueueLen() int { return len(m.queue) }

// worker 数量 = MaxConcurrency（≤2），与 Agent Loop 共享同一并发预算。
func (m *ChannelManager) worker() {
	for task := range m.queue {
		ctx, cancel := context.WithTimeout(context.Background(), m.timeout)
		reply, err := m.app.Handle(ctx, task.text)
		if err != nil {
			log.Printf("渠道 %s 处理失败：%v", task.ch.Name(), err)
			reply = "处理失败：" + err.Error()
		}
		if sendErr := task.ch.Send(ctx, task.target, reply, task.extra); sendErr != nil {
			log.Printf("渠道 %s 回复失败：%v", task.ch.Name(), sendErr)
		}
		cancel()
	}
}

// Submit 先 ACK 再异步处理：平台回调普遍 1~5 秒超时，LLM 调用不能挡在回调里。
// 队列满时不记去重键，让平台重试时仍能入队。
func (m *ChannelManager) Submit(t inbound) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if t.eventID != "" {
		if _, dup := m.seen[t.eventID]; dup {
			return true
		}
	}
	select {
	case m.queue <- t:
		m.markLocked(t.eventID)
		return true
	default:
		return false
	}
}

func (m *ChannelManager) markLocked(id string) {
	if id == "" {
		return
	}
	if len(id) > 128 {
		h := sha256.Sum256([]byte(id))
		id = hex.EncodeToString(h[:])
	}
	m.seen[id] = time.Now().Unix()
	m.order = append(m.order, id)
	if len(m.order) > m.maxSeen {
		cut := len(m.order) - m.maxSeen
		for _, k := range m.order[:cut] {
			delete(m.seen, k)
		}
		m.order = append([]string(nil), m.order[cut:]...)
	}
}

// ---------- 通用 HTTP 工具（全部带超时与响应体上限） ----------

func readBody(r *http.Request, max int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r.Body, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("请求体超过 %d 字节预算", max)
	}
	return b, nil
}

func ackOK(w http.ResponseWriter) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func ackJSON(w http.ResponseWriter, v interface{}) {
	writeJSON(w, http.StatusOK, v)
}

func reject(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func (m *ChannelManager) doJSON(ctx context.Context, method, url string, headers map[string]string, payload interface{}, maxResp int64) ([]byte, error) {
	var reader io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResp+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxResp {
		return nil, errors.New("平台响应超过预算，已丢弃")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("平台 HTTP %d：%s", resp.StatusCode, truncate(string(data), 200))
	}
	return data, nil
}

// tokenCache：平台 access_token 有效期通常 7200 秒，提前 5 分钟刷新。
// 单飞（锁内取）避免 1 GB 机器上并发重复取 token。
type tokenCache struct {
	mu    sync.Mutex
	val   string
	exp   time.Time
	fetch func(context.Context) (string, int, error)
}

func (t *tokenCache) Get(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.val != "" && time.Now().Before(t.exp) {
		return t.val, nil
	}
	v, secs, err := t.fetch(ctx)
	if err != nil {
		return "", err
	}
	if secs <= 0 {
		secs = 7200
	}
	if secs > 300 {
		secs -= 300
	}
	t.val = v
	t.exp = time.Now().Add(time.Duration(secs) * time.Second)
	return v, nil
}

// Notify 把文本推到单向通知出口（微信服务号推送 / 群机器人 / 自定义 Webhook）。
func (m *ChannelManager) Notify(ctx context.Context, title, text string) error {
	return notifySend(m, ctx, title, text)
}
