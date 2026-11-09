package cx

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

const (
	StrategySerial    = "serial"
	StrategyParallel  = "parallel"
	StrategyHierarchy = "hierarchy"
	StrategyDebate    = "debate"
	StrategyPipeline  = "pipeline"
)

// ParseStrategy 支持显式 @team:<strategy> 触发，不引入分类模型。
func ParseStrategy(text string) (string, string) {
	low := strings.ToLower(text)
	idx := strings.Index(low, "@team:")
	if idx < 0 {
		return "", text
	}
	rest := text[idx+len("@team:"):]
	parts := strings.SplitN(rest, " ", 2)
	s := strings.ToLower(strings.TrimSpace(parts[0]))
	body := ""
	if len(parts) > 1 {
		body = strings.TrimSpace(parts[1])
	}
	if body == "" {
		return "", text
	}
	switch s {
	case StrategySerial, StrategyParallel, StrategyHierarchy, StrategyDebate, StrategyPipeline:
		return s, body
	default:
		return "", text
	}
}

func (a *App) teamMembers() []AgentDef {
	defs := a.reg.All()
	subs := make([]AgentDef, 0, 3)
	for _, d := range defs {
		if d.ID != "main" {
			subs = append(subs, d)
		}
	}
	if len(subs) > 3 {
		subs = subs[:3]
	}
	return subs
}

// ask 每个子 Agent 是一次独立会话，system 可以不同；主会话的 L0 前缀不受影响。
func (a *App) ask(ctx context.Context, d AgentDef, task string, strategy string) (string, error) {
	cap := a.cfg.Budgets.MaxSubagentChars
	user := BuildContext(nil, &d, strategy, nil, nil, nil, cap/2) + "\n任务：" + truncate(task, cap/2)
	sys := a.system + "\n\n" + d.System
	return a.prov.Chat(ctx, sys, []Turn{{Role: "user", Content: user}})
}

func (a *App) runTeam(ctx context.Context, strategy, text string) (string, error) {
	subs := a.teamMembers()
	if len(subs) == 0 {
		return a.runSingle(ctx, text)
	}
	limit := a.cfg.Budgets.MaxConcurrency
	if (strategy == StrategyParallel || strategy == StrategyDebate) && limit < 2 {
		// 1 GB 机器上并发被限到 1 时，明确退化而不是假装并行。
		strategy = StrategySerial
	}
	switch strategy {
	case StrategyParallel:
		return a.teamParallel(ctx, text, subs)
	case StrategyDebate:
		return a.teamDebate(ctx, text, subs)
	case StrategyHierarchy:
		return a.teamHierarchy(ctx, text, subs)
	case StrategyPipeline, StrategySerial:
		return a.teamPipeline(ctx, text, subs)
	default:
		return a.teamPipeline(ctx, text, subs)
	}
}

func (a *App) teamPipeline(ctx context.Context, text string, subs []AgentDef) (string, error) {
	var b strings.Builder
	b.WriteString("流水线（串行/流水线语义，单并发）：\n")
	acc := text
	for _, d := range subs {
		task := "原始目标：" + text + "\n\n上一步产出：\n" + truncate(acc, 1200)
		r, err := a.ask(ctx, d, task, StrategyPipeline)
		if err != nil {
			fmt.Fprintf(&b, "[%s] 失败：%v\n", d.Name, err)
			continue
		}
		acc = r
		fmt.Fprintf(&b, "[%s] %s\n\n", d.Name, truncate(r, 900))
	}
	return b.String(), nil
}

func (a *App) teamParallel(ctx context.Context, text string, subs []AgentDef) (string, error) {
	var wg sync.WaitGroup
	results := make([]string, len(subs))
	errs := make([]error, len(subs))
	for i, d := range subs {
		wg.Add(1)
		go func(i int, d AgentDef) {
			defer wg.Done()
			a.sem <- struct{}{}
			defer func() { <-a.sem }()
			results[i], errs[i] = a.ask(ctx, d, text, StrategyParallel)
		}(i, d)
	}
	wg.Wait()
	var b strings.Builder
	fmt.Fprintf(&b, "并行结果（全局并发上限 %d）：\n", cap(a.sem))
	for i, d := range subs {
		if errs[i] != nil {
			fmt.Fprintf(&b, "- %s：失败 %v\n", d.Name, errs[i])
			continue
		}
		fmt.Fprintf(&b, "- %s：%s\n", d.Name, truncate(results[i], 800))
	}
	return b.String(), nil
}

func (a *App) teamHierarchy(ctx context.Context, text string, subs []AgentDef) (string, error) {
	main := a.reg.Main()
	plan, err := a.ask(ctx, main, "把下面目标拆成不超过 3 条互不依赖的子任务，每行一条，不要解释：\n"+text, StrategyHierarchy)
	if err != nil {
		return a.teamPipeline(ctx, text, subs)
	}
	lines := strings.Split(strings.TrimSpace(plan), "\n")
	if len(lines) > len(subs) {
		lines = lines[:len(subs)]
	}
	var b strings.Builder
	b.WriteString("层级分解：\n" + truncate(plan, 600) + "\n\n执行结果：\n")
	for i, line := range lines {
		r, err := a.ask(ctx, subs[i%len(subs)], truncate(line, 400), StrategyHierarchy)
		if err != nil {
			fmt.Fprintf(&b, "- %s：失败 %v\n", subs[i%len(subs)].Name, err)
			continue
		}
		fmt.Fprintf(&b, "- %s：%s\n", subs[i%len(subs)].Name, truncate(r, 600))
	}
	return b.String(), nil
}

func (a *App) teamDebate(ctx context.Context, text string, subs []AgentDef) (string, error) {
	var b strings.Builder
	b.WriteString("辩论（两轮，限并发）：\n")
	var views []string
	for round := 0; round < 2; round++ {
		prev := ""
		if len(views) > 0 {
			prev = "\n对方观点：\n" + truncate(views[len(views)-1], 800)
		}
		d := subs[round%len(subs)]
		r, err := a.ask(ctx, d, text+prev, StrategyDebate)
		if err != nil {
			fmt.Fprintf(&b, "第 %d 轮失败：%v\n", round+1, err)
			continue
		}
		views = append(views, r)
		fmt.Fprintf(&b, "第 %d 轮 [%s]：%s\n\n", round+1, d.Name, truncate(r, 700))
	}
	if len(views) >= 2 {
		b.WriteString("共识待用户判断：以上两轮观点并列呈现，晨曦不替你拍板。\n")
	}
	return b.String(), nil
}
