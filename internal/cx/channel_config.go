package cx

// 所有密钥都只写「环境变量名」，值从进程环境读取，绝不落进 config.json。
// 渠道默认全部关闭：现有部署行为不变，也避免把 7788 裸奔到公网。

type FeishuConf struct {
	Enabled        bool   `json:"enabled"`
	Path           string `json:"path"`
	APIBase        string `json:"api_base"`
	AppIDEnv       string `json:"app_id_env"`
	AppSecretEnv   string `json:"app_secret_env"`
	EncryptKeyEnv  string `json:"encrypt_key_env"`
	VerifyTokenEnv string `json:"verify_token_env"`
	DefaultTarget  string `json:"default_target"`
}

type WeComConf struct {
	Enabled       bool   `json:"enabled"`
	Path          string `json:"path"`
	APIBase       string `json:"api_base"`
	CorpID        string `json:"corp_id"`
	AgentID       string `json:"agent_id"`
	TokenEnv      string `json:"token_env"`
	AESKeyEnv     string `json:"aes_key_env"`
	SecretEnv     string `json:"secret_env"`
	DefaultTarget string `json:"default_target"`
}

type QQConf struct {
	Enabled   bool   `json:"enabled"`
	Path      string `json:"path"`
	APIBase   string `json:"api_base"`
	AppIDEnv  string `json:"app_id_env"`
	SecretEnv string `json:"secret_env"`
	Sandbox   bool   `json:"sandbox"`
}

type WebhookConf struct {
	Enabled   bool   `json:"enabled"`
	Path      string `json:"path"`
	SecretEnv string `json:"secret_env"`
}

type NotifyConf struct {
	Enabled bool   `json:"enabled"`
	Kind    string `json:"kind"`
	URLEnv  string `json:"url_env"`
	Title   string `json:"title"`
}

type ChannelsConfig struct {
	MaxDedup         int          `json:"max_dedup"`
	ReplyTimeoutSec  int          `json:"reply_timeout_sec"`
	MaxCallbackBytes int          `json:"max_callback_bytes"`
	Feishu           FeishuConf   `json:"feishu"`
	WeCom            WeComConf    `json:"wecom"`
	QQ               QQConf       `json:"qq"`
	Webhook          WebhookConf  `json:"webhook"`
	Notify           NotifyConf   `json:"notify"`
}

func applyChannelDefaults(c *ChannelsConfig) {
	setInt(&c.MaxDedup, 256)
	if c.MaxDedup > 2048 {
		c.MaxDedup = 2048
	}
	setInt(&c.ReplyTimeoutSec, 60)
	if c.ReplyTimeoutSec > 120 {
		c.ReplyTimeoutSec = 120
	}
	setInt(&c.MaxCallbackBytes, 65536)
	if c.MaxCallbackBytes > 262144 {
		c.MaxCallbackBytes = 262144
	}
	setStr(&c.Feishu.Path, "/v1/channel/feishu")
	setStr(&c.Feishu.APIBase, "https://open.feishu.cn")
	setStr(&c.Feishu.AppIDEnv, "CHENXI_FEISHU_APP_ID")
	setStr(&c.Feishu.AppSecretEnv, "CHENXI_FEISHU_APP_SECRET")
	setStr(&c.WeCom.Path, "/v1/channel/wecom")
	setStr(&c.WeCom.APIBase, "https://qyapi.weixin.qq.com")
	setStr(&c.WeCom.TokenEnv, "CHENXI_WECOM_TOKEN")
	setStr(&c.WeCom.AESKeyEnv, "CHENXI_WECOM_AES_KEY")
	setStr(&c.WeCom.SecretEnv, "CHENXI_WECOM_SECRET")
	setStr(&c.QQ.Path, "/v1/channel/qq")
	setStr(&c.QQ.APIBase, "https://api.bot.qq.com")
	setStr(&c.QQ.AppIDEnv, "CHENXI_QQ_APP_ID")
	setStr(&c.QQ.SecretEnv, "CHENXI_QQ_SECRET")
	if c.QQ.Sandbox && c.QQ.APIBase == "https://api.bot.qq.com" {
		c.QQ.APIBase = "https://sandbox.api.sgroup.qq.com"
	}
	setStr(&c.Webhook.Path, "/v1/channel/webhook")
	setStr(&c.Webhook.SecretEnv, "CHENXI_WEBHOOK_SECRET")
	setStr(&c.Notify.Kind, "custom")
	setStr(&c.Notify.URLEnv, "CHENXI_NOTIFY_URL")
	setStr(&c.Notify.Title, "晨曦")
}
