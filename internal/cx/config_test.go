package cx

import (
	"os"
	"path/filepath"
	"testing"
)

func writeCfg(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadConfigAppliesDefaults(t *testing.T) {
	p := writeCfg(t, `{"provider":{"kind":"openai"}}`)
	c, err := LoadConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != "127.0.0.1:7788" || c.Socket == "" || c.DataDir == "" {
		t.Fatalf("defaults missing: %+v", c)
	}
	if c.Provider.TimeoutSec != 60 || c.Provider.MaxResponseBytes != 65536 {
		t.Fatalf("provider defaults wrong: %+v", c.Provider)
	}
	if c.Host.DefaultApproval != "ask" || c.Host.Workers != 2 {
		t.Fatalf("host defaults wrong: %+v", c.Host)
	}
}

func TestLoadConfigHardCapsAreEnforced(t *testing.T) {
	p := writeCfg(t, `{"provider":{"kind":"openai"},"budgets":{"max_concurrency":99,"go_mem_limit_mib":999,"max_steps":0}}`)
	c, err := LoadConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Budgets.MaxConcurrency != 2 {
		t.Fatalf("并发上限未钳制到 2，得到 %d", c.Budgets.MaxConcurrency)
	}
	if c.Budgets.GoMemLimitMiB != 256 {
		t.Fatalf("堆上限未钳制到 256，得到 %d", c.Budgets.GoMemLimitMiB)
	}
	if c.Budgets.MaxSteps != 8 {
		t.Fatalf("默认步数未补齐，得到 %d", c.Budgets.MaxSteps)
	}
}

func TestLoadConfigRejectsLocalLLM(t *testing.T) {
	p := writeCfg(t, `{"provider":{"kind":"local"}}`)
	if _, err := LoadConfig(p); err == nil {
		t.Fatal("1 GB 机器上必须拒绝本地推理")
	}
}

func TestLoadConfigRejectsUnknownProvider(t *testing.T) {
	p := writeCfg(t, `{"provider":{"kind":"mystery"}}`)
	if _, err := LoadConfig(p); err == nil {
		t.Fatal("未知 provider 必须拒绝")
	}
}

func TestLoadConfigRejectsBadJSON(t *testing.T) {
	p := writeCfg(t, `not-json`)
	if _, err := LoadConfig(p); err == nil {
		t.Fatal("非法 JSON 必须报错")
	}
}
