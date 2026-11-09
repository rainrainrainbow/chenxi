package cx

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

type Turn struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Provider struct {
	cfg    ProviderConfig
	client *http.Client
}

func NewProvider(c ProviderConfig) (*Provider, error) {
	if c.Kind == "local" {
		return nil, fmt.Errorf("1 GB RAM 下禁用本地推理：请用云端或局域网端点")
	}
	if c.Kind != "openai" {
		return nil, fmt.Errorf("unsupported provider kind %q", c.Kind)
	}
	return &Provider{
		cfg: c,
		client: &http.Client{
			Timeout: time.Duration(c.TimeoutSec) * time.Second,
		},
	}, nil
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// Chat 走 OpenAI 兼容接口。system 与 turns 分离，保证同一会话的
// system 前缀逐字节稳定（prompt cache 可复用）。
func (p *Provider) Chat(ctx context.Context, system string, turns []Turn) (string, error) {
	key := os.Getenv(p.cfg.APIKeyEnv)
	if key == "" {
		return "", fmt.Errorf("环境变量 %s 未设置：晨曦不把密钥写进配置文件", p.cfg.APIKeyEnv)
	}
	msgs := make([]map[string]string, 0, len(turns)+1)
	msgs = append(msgs, map[string]string{"role": "system", "content": system})
	for _, t := range turns {
		msgs = append(msgs, map[string]string{"role": t.Role, "content": t.Content})
	}
	body := map[string]interface{}{
		"model":      p.cfg.Model,
		"messages":   msgs,
		"stream":     false,
		"max_tokens": 1024,
	}
	b, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	url := strings.TrimRight(p.cfg.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := p.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, int64(p.cfg.MaxResponseBytes)+1))
	if err != nil {
		return "", err
	}
	if len(data) > p.cfg.MaxResponseBytes {
		return "", fmt.Errorf("模型响应超过 %d 字节预算，已丢弃", p.cfg.MaxResponseBytes)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("provider HTTP %d: %s", resp.StatusCode, truncate(string(data), 240))
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", fmt.Errorf("解析模型响应: %w", err)
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("模型返回空 choices")
	}
	return out.Choices[0].Message.Content, nil
}
