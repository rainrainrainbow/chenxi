package cx

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type toolCall struct {
	Tool string `json:"tool"`
	Arg  string `json:"arg"`
	Mode string `json:"mode"`
}

func extractToolCall(s string) (toolCall, bool) {
	i := strings.Index(s, "{")
	j := strings.LastIndex(s, "}")
	if i < 0 || j <= i {
		return toolCall{}, false
	}
	var tc toolCall
	if json.Unmarshal([]byte(s[i:j+1]), &tc) != nil {
		return toolCall{}, false
	}
	if tc.Tool == "" {
		return toolCall{}, false
	}
	return tc, true
}

func mapToolOp(tool string) string {
	switch tool {
	case "read", "write", "exec", "net":
		return tool
	default:
		return "unknown"
	}
}

// Handle 是唯一入口：清洗 → 热重载 → 单写者锁 → 单 Agent 或团队 → 落盘。
func (a *App) Handle(ctx context.Context, raw string) (string, error) {
	text := Sanitize(raw, a.cfg.Budgets.MaxMessageBytes)
	if text == "" {
		return "", fmt.Errorf("空消息")
	}
	if changed, err := a.reg.ReloadIfChanged(); changed {
		if err != nil {
			return "", fmt.Errorf("Agent 注册表热重载失败: %w", err)
		}
	}
	strategy, body := ParseStrategy(text)
	if strategy != "" {
		text = body
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if strategy != "" {
		reply, err := a.runTeam(ctx, strategy, text)
		if err != nil {
			return "", err
		}
		_ = a.store.Append("message", "团队任务: "+text+"\n晨曦: "+truncate(reply, 2000),
			map[string]string{"strategy": strategy})
		return reply, nil
	}
	return a.runSingle(ctx, text)
}

func (a *App) runSingle(ctx context.Context, text string) (string, error) {
	mainDef := a.reg.Main()
	scene := a.cfg.SceneFor(text)
	recalled := a.store.Search(text, 3)
	ctxBlock := BuildContext(scene, &mainDef, "", a.store.Corrections(),
		RecallTriples(recalled, 3), recalled, a.cfg.Budgets.MaxContextChars/3)

	turns := []Turn{{Role: "user", Content: ctxBlock + "\n\n用户：" + text}}
	used := make([]string, 0, 4)
	reply := ""

	for step := 0; step < a.cfg.Budgets.MaxSteps; step++ {
		out, err := a.prov.Chat(ctx, a.system, turns)
		if err != nil {
			return "", err
		}
		tc, ok := extractToolCall(out)
		if !ok {
			reply = out
			break
		}
		mode := tc.Mode
		if mode == "" {
			mode = a.cfg.Host.DefaultMode
		}
		r, callErr := a.host.Call(ctx, mapToolOp(tc.Tool), tc.Tool, tc.Arg, mode, a.cfg.Host.DefaultApproval)
		result := ""
		if callErr != nil {
			result = "hostd 不可达：" + callErr.Error()
		} else if !r.OK {
			result = "被拒绝或失败：" + r.Error
		} else {
			result = r.Data
			used = append(used, tc.Tool)
		}
		turns = append(turns,
			Turn{Role: "assistant", Content: out},
			Turn{Role: "user", Content: "[工具结果] " + truncate(result, a.cfg.Budgets.MaxToolOutputBytes)})
	}
	if reply == "" {
		reply = "已达到最大步数上限，本轮停止。若需要继续，请再说一次。"
	}

	sceneID := ""
	if scene != nil {
		sceneID = scene.ID
	}
	_ = a.store.Append("message", "用户: "+text+"\n晨曦: "+reply,
		map[string]string{"scene": sceneID})
	a.skills.Observe(used, len(used) > 0)

	// 纠正一次，永久生效：写成图谱里的 correction 关系，下一轮注入上下文。
	if strings.Contains(text, "不对") || strings.Contains(text, "记住") || strings.Contains(text, "以后别") {
		a.store.AddTriple("user", "correction", truncate(text, 120))
	}
	return reply, nil
}
