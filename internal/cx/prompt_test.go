package cx

import (
	"strings"
	"testing"
)

func TestSanitizeStripsControlChars(t *testing.T) {
	out := Sanitize("  hi\x00\x01\n\t中文  ", 100)
	if strings.Contains(out, "\x00") || strings.Contains(out, "\x01") {
		t.Fatalf("控制字符未清除: %q", out)
	}
	if !strings.Contains(out, "中文") || !strings.Contains(out, "\n") || !strings.Contains(out, "\t") {
		t.Fatalf("合法内容被误删: %q", out)
	}
	if strings.HasPrefix(out, " ") || strings.HasSuffix(out, " ") {
		t.Fatalf("首尾空白未清理: %q", out)
	}
}

func TestSanitizeTruncates(t *testing.T) {
	out := Sanitize(strings.Repeat("汉", 500), 20)
	if len([]rune(out)) > 21 {
		t.Fatalf("未按字符上限截断: %d", len([]rune(out)))
	}
}

func TestSceneFor(t *testing.T) {
	c := &Config{Scenes: []Scene{
		{ID: "ops", Keywords: []string{"磁盘", "内存"}},
		{ID: "coding", Keywords: []string{"代码", "编译"}},
	}}
	if s := c.SceneFor("看看磁盘还剩多少"); s == nil || s.ID != "ops" {
		t.Fatalf("ops 场景未命中: %+v", s)
	}
	if s := c.SceneFor("随便聊聊天气"); s != nil {
		t.Fatalf("无关键词时不应命中场景: %+v", s)
	}
}

func TestBuildSystemIsDeterministic(t *testing.T) {
	a := BuildSystem("SOUL", "AGENTS")
	b := BuildSystem("SOUL", "AGENTS")
	if a != b {
		t.Fatal("L0 前缀必须逐字节稳定")
	}
	if !strings.Contains(a, "1 GB 内存") {
		t.Fatal("硬约束未注入 L0")
	}
}

func TestParseStrategy(t *testing.T) {
	s, body := ParseStrategy("帮我 @team:parallel 分析这两个方案")
	if s != StrategyParallel || body != "分析这两个方案" {
		t.Fatalf("策略解析错误: s=%q body=%q", s, body)
	}
	if s2, _ := ParseStrategy("普通一句话"); s2 != "" {
		t.Fatalf("无标记时不应触发团队: %q", s2)
	}
	if s3, _ := ParseStrategy("@team:nonsense 干活"); s3 != "" {
		t.Fatalf("未知策略应回退单 Agent: %q", s3)
	}
}
