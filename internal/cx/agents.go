package cx

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

type AgentDef struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Role    string   `json:"role"`
	System  string   `json:"system"`
	Tools   []string `json:"tools"`
	Mode    string   `json:"mode"`
	Builtin bool     `json:"builtin"`
}

// Registry 借鉴璇玑 AgentRegistry：声明式配置 + 哈希比对热重载。
// 用 5 秒节流的轮询替代 fsnotify，省掉一个依赖。
type Registry struct {
	path    string
	mu      sync.RWMutex
	defs    map[string]AgentDef
	hash    [32]byte
	lastChk time.Time
}

func NewRegistry(path string) (*Registry, error) {
	r := &Registry{path: path, defs: make(map[string]AgentDef)}
	if err := r.Reload(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Registry) Reload() error {
	b, err := os.ReadFile(r.path)
	if err != nil {
		return fmt.Errorf("read agents: %w", err)
	}
	var list []AgentDef
	if err := json.Unmarshal(b, &list); err != nil {
		return fmt.Errorf("parse agents: %w", err)
	}
	m := make(map[string]AgentDef, len(list))
	for _, d := range list {
		if d.ID == "" {
			continue
		}
		if d.Mode == "" {
			d.Mode = "readonly"
		}
		m[d.ID] = d
	}
	if len(m) == 0 {
		return fmt.Errorf("agents.json 中没有可用 Agent")
	}
	r.mu.Lock()
	r.defs = m
	r.hash = sha256.Sum256(b)
	r.mu.Unlock()
	return nil
}

func (r *Registry) ReloadIfChanged() (bool, error) {
	r.mu.Lock()
	if time.Since(r.lastChk) < 5*time.Second {
		r.mu.Unlock()
		return false, nil
	}
	r.lastChk = time.Now()
	r.mu.Unlock()

	b, err := os.ReadFile(r.path)
	if err != nil {
		return false, err
	}
	r.mu.RLock()
	same := sha256.Sum256(b) == r.hash
	r.mu.RUnlock()
	if same {
		return false, nil
	}
	return true, r.Reload()
}

func (r *Registry) Get(id string) (AgentDef, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	d, ok := r.defs[id]
	return d, ok
}

func (r *Registry) Main() AgentDef {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if d, ok := r.defs["main"]; ok {
		return d
	}
	for _, d := range r.defs {
		return d
	}
	return AgentDef{ID: "main", Name: "晨曦", Mode: "workspace"}
}

func (r *Registry) All() []AgentDef {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]AgentDef, 0, len(r.defs))
	for _, d := range r.defs {
		out = append(out, d)
	}
	return out
}
