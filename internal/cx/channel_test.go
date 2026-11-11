package cx

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
)

func testEncrypt(key, iv, plain []byte, padMultiple int) []byte {
	n := padMultiple - len(plain)%padMultiple
	if n == 0 {
		n = padMultiple
	}
	padded := append(append([]byte{}, plain...), bytes.Repeat([]byte{byte(n)}, n)...)
	block, err := aes.NewCipher(key)
	if err != nil {
		panic(err)
	}
	out := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(out, padded)
	return out
}

func TestFeishuDecryptRoundtrip(t *testing.T) {
	key := "cx-test-encrypt-key"
	iv := []byte("0123456789abcdef")
	event := []byte(`{"type":"url_verification","challenge":"abc123"}`)
	cipherText := testEncrypt(feishuKey(key), iv, event, 16)
	b64 := base64.StdEncoding.EncodeToString(append(iv, cipherText...))
	got, err := decryptFeishu(key, b64)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(event) {
		t.Fatalf("飞书解密结果不符：%q", got)
	}
	if got2, err := decryptFeishu("wrong-key", b64); err == nil && string(got2) == string(event) {
		t.Fatal("错误 Encrypt Key 不得还原出原文")
	}
}

func TestWecomDecryptRoundtrip(t *testing.T) {
	encodingAESKey := "jWmYm7qr5nMoAUwZRjGtBxmz3KA1tkAj3ykkR6q2B2C"
	key, err := wecomKey(encodingAESKey)
	if err != nil {
		t.Fatal(err)
	}
	msg := []byte(`<xml><MsgType>text</MsgType><Content>你好晨曦</Content></xml>`)
	plain := make([]byte, 0, 16+4+len(msg)+9)
	plain = append(plain, bytes.Repeat([]byte{'r'}, 16)...)
	l := len(msg)
	plain = append(plain, byte(l>>24), byte(l>>16), byte(l>>8), byte(l))
	plain = append(plain, msg...)
	plain = append(plain, []byte("wwcorpid")...)
	// 企微 PKCS7 按 32 字节分组填充。
	cipherText := testEncrypt(key, key[:16], plain, 32)
	b64 := base64.StdEncoding.EncodeToString(cipherText)
	got, err := decryptWecom(encodingAESKey, b64, "wwcorpid")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(msg) {
		t.Fatalf("企微解密结果不符：%q", got)
	}
	if _, err := decryptWecom(encodingAESKey, b64, "other-corpid"); err == nil {
		t.Fatal("receiveid 不匹配必须拒绝")
	}
	if _, err := decryptWecom("short", b64, "wwcorpid"); err == nil {
		t.Fatal("非法 EncodingAESKey 必须拒绝")
	}
}

func TestWecomSignatureIsOrderIndependent(t *testing.T) {
	a := wecomSignature("tok", "1409659813", "1372623149", "ENCRYPT")
	b := wecomSignature("1372623149", "ENCRYPT", "tok", "1409659813")
	if a != b {
		t.Fatal("验签参数必须按字典序拼接，与传入顺序无关")
	}
	if len(a) != 40 {
		t.Fatalf("sha1 十六进制长度应为 40，实际 %d", len(a))
	}
	if wecomSignature("tok", "1409659813", "1372623149", "OTHER") == a {
		t.Fatal("密文不同时签名必须不同")
	}
}

func TestQQSignVerify(t *testing.T) {
	pub, priv := qqKeys("naOC0ocQE3shWLAfffVLB1rhYPG7")
	ts := "1725442341"
	plainToken := "plain-token-abc"

	// 回调地址验证：私钥签 event_ts + plain_token，平台用派生公钥验。
	validationSig, err := hex.DecodeString(qqSign(priv, ts, plainToken))
	if err != nil || !ed25519.Verify(pub, []byte(ts+plainToken), validationSig) {
		t.Fatal("回调验证签名应能被派生公钥验证")
	}
	if ed25519.Verify(pub, []byte(ts+plainToken+"x"), validationSig) {
		t.Fatal("篡改 plain_token 必须通过不了")
	}

	// 事件回调验签：签名体 = timestamp + 原始 body。
	body := []byte(`{"op":0,"d":{},"t":"C2C_MESSAGE_CREATE"}`)
	msg := append([]byte(ts), body...)
	sig := hex.EncodeToString(ed25519.Sign(priv, msg))
	if !qqVerify(pub, sig, ts, body) {
		t.Fatal("合法 Ed25519 签名应通过")
	}
	if qqVerify(pub, sig, ts+"1", body) {
		t.Fatal("篡改时间戳必须通过不了")
	}
	if qqVerify(pub, "not-hex", ts, body) {
		t.Fatal("非法 hex 签名必须通过不了")
	}
	if qqVerify(pub, strings.Repeat("00", 64), ts, body) {
		t.Fatal("全零签名必须通过不了")
	}
	otherPub, _ := qqKeys("another-secret")
	if qqVerify(otherPub, sig, ts, body) {
		t.Fatal("错误 Bot Secret 派生的公钥必须通过不了")
	}
}

func TestHMACAndConstEq(t *testing.T) {
	want := hmacSHA256Hex("secret", []byte("hello"))
	if !constEq(want, hmacSHA256Hex("secret", []byte("hello"))) {
		t.Fatal("相同输入必须产生相同 HMAC")
	}
	if constEq(want, hmacSHA256Hex("secret", []byte("hell0"))) {
		t.Fatal("篡改 body 后 HMAC 必须不同")
	}
	if len(want) != sha256.Size*2 {
		t.Fatalf("HMAC 十六进制长度错误：%d", len(want))
	}
}

func TestPKCS7UnpadRejectsBad(t *testing.T) {
	for _, b := range [][]byte{
		{},
		{1, 2, 3, 0},
		{1, 2, 3, 33},
		{1, 2, 3, 3, 2},
	} {
		if _, err := pkcs7Unpad(b); err == nil {
			t.Fatalf("非法填充应被拒绝：%v", b)
		}
	}
	if got, err := pkcs7Unpad([]byte{1, 2, 3, 2, 2}); err != nil || !bytes.Equal(got, []byte{1, 2, 3}) {
		t.Fatalf("合法填充解析错误：%v %v", got, err)
	}
}

func TestStripMentions(t *testing.T) {
	if got := stripMentions("  @_user_1  帮我查磁盘  "); got != "帮我查磁盘" {
		t.Fatalf("提及清理失败：%q", got)
	}
}

func TestChannelDedupIsBounded(t *testing.T) {
	m := &ChannelManager{
		seen:    make(map[string]int64),
		maxSeen: 3,
		queue:   make(chan inbound, 2),
	}
	if !m.Submit(inbound{eventID: "e1", text: "a"}) {
		t.Fatal("首次提交应成功")
	}
	// 同 eventID 返回 true（ACK）但不重复入队。
	if !m.Submit(inbound{eventID: "e1", text: "a"}) {
		t.Fatal("重复事件应静默 ACK")
	}
	if len(m.queue) != 1 {
		t.Fatalf("去重失效，队列长度 %d", len(m.queue))
	}
	if !m.Submit(inbound{eventID: "e2", text: "b"}) {
		t.Fatal("第二个事件应入队")
	}
	// 队列容量为 2，第三个应被拒（且不能记去重键，留待平台重试）。
	if m.Submit(inbound{eventID: "e3", text: "c"}) {
		t.Fatal("队列满时必须返回 false")
	}
	if _, exists := m.seen["e3"]; exists {
		t.Fatal("队列满时不应记录去重键，否则平台重试会被误丢")
	}
	// 排空后重试 e3 应该能进队。
	<-m.queue
	if !m.Submit(inbound{eventID: "e3", text: "c"}) {
		t.Fatal("队列腾出后重试应成功")
	}
	for i := 0; i < 10; i++ {
		m.markLocked("k" + strings.Repeat("x", i))
	}
	if len(m.seen) > m.maxSeen {
		t.Fatalf("去重集合越过上限：%d > %d", len(m.seen), m.maxSeen)
	}
}

func TestValidateChannelsRejectUnsignedCallbacks(t *testing.T) {
	if err := validateFeishu(&FeishuConf{Path: "/x"}); err == nil {
		t.Fatal("飞书无验签配置必须拒绝启动")
	}
	if err := validateWeCom(&WeComConf{Path: "/x"}); err == nil {
		t.Fatal("企微无验签配置必须拒绝启动")
	}
	if err := validateQQ(&QQConf{Path: "/x"}); err == nil {
		t.Fatal("QQ 无凭据必须拒绝启动")
	}
	t.Setenv("CHENXI_TEST_WEBHOOK_SECRET", "s3cr3t")
	if err := validateWebhook(&WebhookConf{Path: "/x", SecretEnv: "CHENXI_TEST_WEBHOOK_SECRET"}); err != nil {
		t.Fatalf("配好密钥的通用 Webhook 应通过：%v", err)
	}
	if err := validateWebhook(&WebhookConf{Path: "/x", SecretEnv: "CHENXI_TEST_MISSING"}); err == nil {
		t.Fatal("环境变量缺失必须拒绝启动")
	}
	t.Setenv("CHENXI_TEST_QQ_ID", "1024")
	t.Setenv("CHENXI_TEST_QQ_SECRET", "bot-secret")
	if err := validateQQ(&QQConf{Path: "/x", AppIDEnv: "CHENXI_TEST_QQ_ID", SecretEnv: "CHENXI_TEST_QQ_SECRET"}); err != nil {
		t.Fatalf("配好凭据的 QQ 渠道应通过：%v", err)
	}
}

func TestApplyChannelDefaultsAndCaps(t *testing.T) {
	c := &ChannelsConfig{MaxDedup: 99999, ReplyTimeoutSec: 999, MaxCallbackBytes: 999999}
	applyChannelDefaults(c)
	if c.MaxDedup != 2048 {
		t.Fatalf("去重上限未钳制：%d", c.MaxDedup)
	}
	if c.ReplyTimeoutSec != 120 || c.MaxCallbackBytes != 262144 {
		t.Fatalf("渠道预算未钳制：%+v", c)
	}
	if c.Feishu.Path != "/v1/channel/feishu" || c.Webhook.Path != "/v1/channel/webhook" {
		t.Fatal("默认回调路径缺失")
	}
	c2 := &ChannelsConfig{}
	applyChannelDefaults(c2)
	if c2.MaxDedup != 256 || c2.ReplyTimeoutSec != 60 || c2.MaxCallbackBytes != 65536 {
		t.Fatalf("渠道默认值错误：%+v", c2)
	}
}
