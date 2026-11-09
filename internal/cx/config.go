package cx

import (
	"encoding/json"
	"fmt"
	"os"
)

// ProviderConfig 是「模型无关」的云端推理配置；kind=local 会被明确拒绝。
type ProviderConfig struct {
	Kind             string `json:"kind"`
	BaseURL          string `json:"base_url"`
	Model            string `json:"model"`
	APIKeyEnv        string `json:"api_key_env"`
	TimeoutSec       int    `json:"timeout_sec"`
	MaxResponseBytes int    `json:"max_response_bytes"`
}

type HostConfig struct {
	DefaultMode     string `json:"default_mode"`
	DefaultApproval string `json:"default_approval"`
	TimeoutSec      int    `json:"timeout_sec"`
	Workers         int    `json:"workers"`
	WorkspaceDir    string `json:"workspace_dir"`
}

type Budgets struct {
	GoMemLimitMiB      int   `json:"go_mem_limit_mib"`
	GOGC               int   `json:"gogc"`
	MaxMessageBytes    int   `json:"max_message_bytes"`
	MaxToolOutputBytes int   `json:"max_tool_output_bytes"`
	MaxContextChars    int   `json:"max_context_chars"`
	MaxIndexEntries    int   `json:"max_index_entries"`
	MaxLogBytes        int64 `json:"max_log_bytes"`
	MaxSnapshotBytes   int64 `json:"max_snapshot_bytes"`
	MaxSteps           int   `json:"max_steps"`
	MaxConcurrency     int   `json:"max_concurrency"`
	MaxSubagentChars   int   `json:"max_subagent_chars"`
}

type Scene struct {
	ID       string   `json:"id"`
	Keywords []string `json:"keywords"`
	Prompt   string   `json:"prompt"`
}

type CronJob struct {
	ID     string `json:"id"`
	Spec   string `json:"spec"`
	Text   string `json:"text"`
	Target string `json:"target"`
}

type Config struct {
	Listen       string         `json:"listen"`
	Socket       string         `json:"socket"`
	DataDir      string         `json:"data_dir"`
	SoulFile     string         `json:"soul_file"`
	AgentsFile   string         `json:"agents_file"`
	RegistryFile string         `json:"registry_file"`
	Provider     ProviderConfig `json:"provider"`
	Host         HostConfig     `json:"host"`
	Budgets      Budgets        `json:"budgets"`
	Scenes       []Scene        `json:"scenes"`
	Cron         []CronJob      `json:"cron"`
}

func setInt(p *int, def int) {
	if *p <= 0 {
		*p = def
	}
}

func setInt64(p *int64, def int64) {
	if *p <= 0 {
		*p = def
	}
}

func setStr(p *string, def string) {
	if *p == "" {
		*p = def
	}
}

// LoadConfig 读入配置并强制施加 1 GB 机器的硬上限。
// 任何试图把并发调到 3 以上、把本地模型打开的配置都在这里被挡下。
func LoadConfig(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	setStr(&c.Listen, "127.0.0.1:7788")
	setStr(&c.Socket, "/run/chenxi/hostd.sock")
	setStr(&c.DataDir, "/var/lib/chenxi")
	setStr(&c.SoulFile, "SOUL.md")
	setStr(&c.AgentsFile, "AGENTS.md")
	setStr(&c.RegistryFile, "agents.json")
	setStr(&c.Provider.Kind, "openai")
	setStr(&c.Provider.BaseURL, "https://api.openai.com/v1")
	setStr(&c.Provider.Model, "gpt-4o-mini")
	setStr(&c.Provider.APIKeyEnv, "CHENXI_API_KEY")
	setStr(&c.Host.DefaultMode, "workspace")
	setStr(&c.Host.DefaultApproval, "ask")
	setStr(&c.Host.WorkspaceDir, c.DataDir+"/workspace")

	setInt(&c.Provider.TimeoutSec, 60)
	setInt(&c.Provider.MaxResponseBytes, 65536)
	setInt(&c.Host.TimeoutSec, 20)
	setInt(&c.Host.Workers, 2)

	setInt(&c.Budgets.GoMemLimitMiB, 128)
	setInt(&c.Budgets.GOGC, 50)
	setInt(&c.Budgets.MaxMessageBytes, 8192)
	setInt(&c.Budgets.MaxToolOutputBytes, 65536)
	setInt(&c.Budgets.MaxContextChars, 12000)
	setInt(&c.Budgets.MaxIndexEntries, 20000)
	setInt(&c.Budgets.MaxSteps, 8)
	setInt(&c.Budgets.MaxConcurrency, 2)
	setInt(&c.Budgets.MaxSubagentChars, 4000)
	setInt64(&c.Budgets.MaxLogBytes, 268435456)
	setInt64(&c.Budgets.MaxSnapshotBytes, 33554432)

	// 硬护栏：这块板子不允许超过 2 路并发，不允许本地推理。
	if c.Budgets.MaxConcurrency > 2 {
		c.Budgets.MaxConcurrency = 2
	}
	if c.Budgets.MaxConcurrency < 1 {
		c.Budgets.MaxConcurrency = 1
	}
	if c.Budgets.GoMemLimitMiB > 256 {
		c.Budgets.GoMemLimitMiB = 256
	}
	if c.Provider.Kind == "local" {
		return nil, fmt.Errorf("provider.kind=local 被禁用：1 GB RAM 放不下可用量化模型与 KV cache，请改用云端或局域网推理端点")
	}
	if c.Provider.Kind != "openai" {
		return nil, fmt.Errorf("unsupported provider kind %q", c.Provider.Kind)
	}
	return &c, nil
}
