package cx

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"time"
)

type HostReply struct {
	OK    bool
	Data  string
	Error string
	ID    string
}

// HostClient 每次请求开一条短连接：UDS 开销极低，且避免长连接状态
// 在 1 GB 机器上堆积。Go 侧不 import os/exec，这里就是唯一出口。
type HostClient struct {
	socket  string
	timeout time.Duration
	mu      sync.Mutex
	next    int64
}

func NewHostClient(c *Config) *HostClient {
	return &HostClient{
		socket:  c.Socket,
		timeout: time.Duration(c.Host.TimeoutSec) * time.Second,
	}
}

func (h *HostClient) Call(ctx context.Context, op, capName, arg, mode, approval string) (HostReply, error) {
	_ = ctx
	h.mu.Lock()
	h.next++
	id := fmt.Sprintf("%d", h.next)
	h.mu.Unlock()

	req := map[string]string{
		"id":       id,
		"op":       op,
		"cap":      capName,
		"arg":      arg,
		"mode":     mode,
		"approval": approval,
	}
	line, err := json.Marshal(req)
	if err != nil {
		return HostReply{}, err
	}
	conn, err := net.DialTimeout("unix", h.socket, h.timeout)
	if err != nil {
		return HostReply{}, fmt.Errorf("chenxi-hostd 不可达（%s）：%w", h.socket, err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(h.timeout)); err != nil {
		return HostReply{}, err
	}
	if _, err := conn.Write(append(line, '\n')); err != nil {
		return HostReply{}, err
	}
	br := bufio.NewReader(conn)
	respLine, err := br.ReadBytes('\n')
	if err != nil {
		return HostReply{}, fmt.Errorf("读取 hostd 响应: %w", err)
	}
	var m map[string]string
	if err := json.Unmarshal(respLine, &m); err != nil {
		return HostReply{}, fmt.Errorf("hostd 返回非法协议: %w", err)
	}
	return HostReply{
		OK:    m["ok"] == "true",
		Data:  m["data"],
		Error: m["error"],
		ID:    m["id"],
	}, nil
}

func (h *HostClient) Ping(ctx context.Context) error {
	r, err := h.Call(ctx, "ping", "", "", "readonly", "allow")
	if err != nil {
		return err
	}
	if !r.OK {
		return fmt.Errorf("hostd ping: %s", r.Error)
	}
	return nil
}
