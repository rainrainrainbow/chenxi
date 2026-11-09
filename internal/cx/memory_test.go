package cx

import (
	"strings"
	"testing"
)

func newTestStore(t *testing.T, maxLog int64) *Store {
	t.Helper()
	c := &Config{
		DataDir: t.TempDir(),
		Budgets: Budgets{
			MaxIndexEntries: 100,
			MaxLogBytes:     maxLog,
		},
	}
	s, err := NewStore(c)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestTokenizeIncludesChineseBigrams(t *testing.T) {
	toks := tokenize("中文测试")
	set := make(map[string]bool)
	for _, x := range toks {
		set[x] = true
	}
	for _, want := range []string{"中", "文", "测", "试", "中文", "文测", "测试"} {
		if !set[want] {
			t.Fatalf("缺少 token %q，实际 %v", want, toks)
		}
	}
}

func TestAppendAndSearch(t *testing.T) {
	s := newTestStore(t, 1<<20)
	if err := s.Append("message", "部署到 armbian 盒子并开启 zram", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Append("message", "编译 rust hostd 守护进程", nil); err != nil {
		t.Fatal(err)
	}
	hits := s.Search("armbian", 5)
	if len(hits) == 0 {
		t.Fatal("英文关键词未命中")
	}
	hits = s.Search("守护进程", 5)
	if len(hits) == 0 {
		t.Fatal("中文 bigram 未命中")
	}
	if len(s.Recent(1)) != 1 {
		t.Fatal("Recent 返回条数错误")
	}
}

func TestCorrectionsAreRecorded(t *testing.T) {
	s := newTestStore(t, 1<<20)
	s.AddTriple("user", "correction", "以后别用 rm -rf")
	s.AddTriple("user", "likes", "中文")
	got := s.Corrections()
	if len(got) != 1 || !strings.Contains(got[0].Object, "rm -rf") {
		t.Fatalf("纠正项未正确写入图谱: %+v", got)
	}
}

func TestCompactionKeepsLogBounded(t *testing.T) {
	// 故意给极小的日志上限，触发 append → compact 路径。
	s := newTestStore(t, 512)
	for i := 0; i < 40; i++ {
		if err := s.Append("message", strings.Repeat("事件内容填充", 20), nil); err != nil {
			t.Fatal(err)
		}
	}
	st := s.Stats()
	if st["log_kb"] > 64 {
		t.Fatalf("日志压缩后仍然过大: %d KB", st["log_kb"])
	}
	if len(s.Recent(3)) != 3 {
		t.Fatal("压缩后最近事件不可读")
	}
}
