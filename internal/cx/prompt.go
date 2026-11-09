package cx

import (
	"fmt"
	"strings"
	"unicode"
)

// Sanitize 把所有入站文本当不可信输入：去控制字符、限长、去首尾空白。
// 上限按字符数近似（配置字段名沿用 bytes 便于阅读）。
func Sanitize(s string, maxChars int) string {
	var b strings.Builder
	for _, r := range s {
		if r == '\n' || r == '\t' || !unicode.IsControl(r) {
			b.WriteRune(r)
		}
	}
	return truncate(strings.TrimSpace(b.String()), maxChars)
}

func (c *Config) SceneFor(text string) *Scene {
	low := strings.ToLower(text)
	best, score := -1, 0
	for i := range c.Scenes {
		n := 0
		for _, kw := range c.Scenes[i].Keywords {
			if kw != "" && strings.Contains(low, strings.ToLower(kw)) {
				n++
			}
		}
		if n > score {
			score, best = n, i
		}
	}
	if best < 0 {
		return nil
	}
	return &c.Scenes[best]
}

// BuildSystem 只拼 L0：SOUL.md + AGENTS.md + 硬约束。
// 启动时读一次，整个进程生命周期内逐字节不变 —— 这是 prompt 前缀缓存的资产。
func BuildSystem(soul, constitution string) string {
	return strings.TrimSpace(fmt.Sprintf(
		"%s\n\n---\n%s\n\n---\n运行时硬约束：本机 1 GB 内存、无本地模型、无图形界面；"+
			"所有副作用必须经 hostd 批准；超出预算直接说明做不到。",
		strings.TrimSpace(soul), strings.TrimSpace(constitution)))
}

// BuildContext 拼可变的 L1（场景）+ L2（协调）+ 记忆召回 + 纠正项。
// 这些内容放在 user turn 里而不是 system 里：同一会话的历史只追加，
// 前缀因此保持可缓存（OpenClaw / Hermes 共同的不变式）。
func BuildContext(scene *Scene, agent *AgentDef, strategy string, corrections, recalled []Triple, events []Event, maxChars int) string {
	var b strings.Builder
	b.WriteString("[晨曦上下文]\n")
	if agent != nil {
		fmt.Fprintf(&b, "角色：%s（%s）\n", agent.Name, agent.Role)
		b.WriteString(agent.System + "\n")
	}
	if scene != nil {
		fmt.Fprintf(&b, "L1 场景 %s：%s\n", scene.ID, scene.Prompt)
	}
	if strategy != "" {
		fmt.Fprintf(&b, "L2 协作策略：%s。并发上限 2，子任务有长度帽。\n", strategy)
	}
	if len(corrections) > 0 {
		b.WriteString("用户曾纠正过（下次别再犯）：\n")
		for _, t := range corrections {
			fmt.Fprintf(&b, "- %s %s %s\n", t.Subject, t.Relation, t.Object)
		}
	}
	if len(recalled) > 0 {
		b.WriteString("相关记忆：\n")
		for _, t := range recalled {
			fmt.Fprintf(&b, "- %s %s %s\n", t.Subject, t.Relation, t.Object)
		}
	}
	if len(events) > 0 {
		b.WriteString("检索到的历史事件：\n")
		for _, e := range events {
			fmt.Fprintf(&b, "- [%s] %s\n", e.Kind, truncate(e.Text, 160))
		}
	}
	return truncate(b.String(), maxChars)
}

// RecallTriples 把检索命中的事件转成极简图谱三元组，供上下文注入。
func RecallTriples(events []Event, limit int) []Triple {
	out := make([]Triple, 0, limit)
	for _, e := range events {
		if len(out) >= limit {
			break
		}
		out = append(out, Triple{
			Subject:  fmt.Sprintf("event/%d", e.ID),
			Relation: e.Kind,
			Object:   truncate(e.Text, 120),
		})
	}
	return out
}
