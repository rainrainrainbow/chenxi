package cx

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Skill struct {
	ID       string `json:"id"`
	Pattern  string `json:"pattern"`
	Template string `json:"template"`
	Uses     int    `json:"uses"`
	LastUsed int64  `json:"last_used"`
	Version  int    `json:"version"`
}

// SkillBook 对应 Hermes 的「从经验自动创建技能」，但提炼是规则式的：
// 不额外调用 LLM，省下 1 GB 机器上最贵的 token 与内存。
type SkillBook struct {
	mu     sync.Mutex
	path   string
	skills []Skill
	max    int
}

func NewSkillBook(dir string) (*SkillBook, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	s := &SkillBook{path: filepath.Join(dir, "skills.json"), max: 64}
	if b, err := os.ReadFile(s.path); err == nil {
		_ = json.Unmarshal(b, &s.skills)
	}
	return s, nil
}

// Observe 在一轮成功后传入本轮用到的工具序列；长度 >= 2 且未见过才沉淀。
func (s *SkillBook) Observe(seq []string, ok bool) {
	if !ok || len(seq) < 2 {
		return
	}
	pattern := strings.Join(seq, ">")
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.skills {
		if s.skills[i].Pattern == pattern {
			s.skills[i].Uses++
			s.skills[i].LastUsed = time.Now().Unix()
			_ = s.saveLocked()
			return
		}
	}
	if len(s.skills) >= s.max {
		s.skills = s.skills[1:]
	}
	s.skills = append(s.skills, Skill{
		ID:       fmt.Sprintf("sk-%d", time.Now().UnixNano()),
		Pattern:  pattern,
		Template: "按顺序复用：" + pattern,
		Uses:     1,
		LastUsed: time.Now().Unix(),
		Version:  1,
	})
	_ = s.saveLocked()
}

func (s *SkillBook) Match(text string, k int) []Skill {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Skill, 0, k)
	low := strings.ToLower(text)
	for _, sk := range s.skills {
		for _, part := range strings.Split(sk.Pattern, ">") {
			if part != "" && strings.Contains(low, strings.ToLower(part)) {
				out = append(out, sk)
				break
			}
		}
		if len(out) >= k {
			break
		}
	}
	return out
}

func (s *SkillBook) saveLocked() error {
	b, err := json.Marshal(s.skills)
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *SkillBook) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.skills)
}
