package cx

import "testing"

func TestSkillBookObserveAndMatch(t *testing.T) {
	book, err := NewSkillBook(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	book.Observe([]string{"read", "exec"}, true)
	if book.Count() != 1 {
		t.Fatalf("技能未沉淀: count=%d", book.Count())
	}
	// 同一序列只更新使用计数，不重复创建。
	book.Observe([]string{"read", "exec"}, true)
	if book.Count() != 1 {
		t.Fatalf("重复序列应复用技能: count=%d", book.Count())
	}
	// 单步或失败序列不沉淀。
	book.Observe([]string{"exec"}, true)
	book.Observe([]string{"read", "write"}, false)
	if book.Count() != 1 {
		t.Fatalf("不该沉淀的技能被写入: count=%d", book.Count())
	}
	if len(book.Match("请 exec 一下", 3)) == 0 {
		t.Fatal("技能未能按关键词召回")
	}
}
