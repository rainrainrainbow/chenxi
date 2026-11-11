package cx

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// 本文件只用标准库实现三家平台的验签/加解密，保持「零第三方依赖」不变。

func feishuKey(encryptKey string) []byte {
	h := sha256.Sum256([]byte(encryptKey))
	return h[:]
}

// wecomKey：AESKey = Base64Decode(EncodingAESKey + "=")，固定 32 字节。
func wecomKey(encodingAESKey string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(encodingAESKey + "=")
	if err != nil {
		return nil, err
	}
	if len(raw) != 32 {
		return nil, fmt.Errorf("wecom AESKey 必须 32 字节，实际 %d", len(raw))
	}
	return raw, nil
}

// pkcs7Unpad 兼容企微的 32 字节分组填充（填充长度可能 > 16）。
func pkcs7Unpad(b []byte) ([]byte, error) {
	if len(b) == 0 {
		return nil, errors.New("empty plaintext")
	}
	n := int(b[len(b)-1])
	if n < 1 || n > 32 || n > len(b) {
		return nil, errors.New("invalid PKCS7 padding")
	}
	for _, c := range b[len(b)-n:] {
		if int(c) != n {
			return nil, errors.New("invalid PKCS7 padding")
		}
	}
	return b[:len(b)-n], nil
}

func aesCBCDecrypt(key, iv, ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	if len(iv) != aes.BlockSize {
		return nil, errors.New("bad iv")
	}
	if len(ciphertext) == 0 || len(ciphertext)%aes.BlockSize != 0 {
		return nil, errors.New("ciphertext not aligned")
	}
	out := make([]byte, len(ciphertext))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(out, ciphertext)
	return pkcs7Unpad(out)
}

// decryptFeishu：密文 = base64(iv[16] + AES-256-CBC(事件JSON))，key = SHA256(EncryptKey)。
func decryptFeishu(encryptKey, b64 string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, err
	}
	if len(raw) <= aes.BlockSize {
		return nil, errors.New("feishu ciphertext too short")
	}
	return aesCBCDecrypt(feishuKey(encryptKey), raw[:aes.BlockSize], raw[aes.BlockSize:])
}

// decryptWecom：明文 = random(16B) + msg_len(4B, 大端) + msg(XML) + receiveid。
func decryptWecom(encodingAESKey, b64, expectReceiveID string) ([]byte, error) {
	key, err := wecomKey(encodingAESKey)
	if err != nil {
		return nil, err
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, err
	}
	plain, err := aesCBCDecrypt(key, key[:aes.BlockSize], raw)
	if err != nil {
		return nil, err
	}
	if len(plain) < 20 {
		return nil, errors.New("wecom plaintext too short")
	}
	n := int(uint32(plain[16])<<24 | uint32(plain[17])<<16 | uint32(plain[18])<<8 | uint32(plain[19]))
	if 20+n > len(plain) {
		return nil, errors.New("wecom msg_len out of range")
	}
	if expectReceiveID != "" && !constEq(string(plain[20+n:]), expectReceiveID) {
		return nil, errors.New("wecom receiveid mismatch")
	}
	return plain[20 : 20+n], nil
}

// wecomSignature：sha1(sort(token, timestamp, nonce, msg_encrypt) 字典序拼接)。
func wecomSignature(token, ts, nonce, encrypt string) string {
	parts := []string{token, ts, nonce, encrypt}
	sort.Strings(parts)
	h := sha1.Sum([]byte(strings.Join(parts, "")))
	return hex.EncodeToString(h[:])
}

// qqKeys：Bot Secret 重复填充到 32 字节作为 Ed25519 seed，派生公私钥。
func qqKeys(secret string) (ed25519.PublicKey, ed25519.PrivateKey) {
	seed := secret
	if len(seed) == 0 {
		// 防御：空串重复永远还是空串，会死循环。启动校验本应拦住，
		// 这里再兜一层，保证任何调用路径都不会挂住进程。
		seed = "chenxi-empty-bot-secret"
	}
	for len(seed) < ed25519.SeedSize {
		seed = strings.Repeat(seed, 2)
	}
	seed = seed[:ed25519.SeedSize]
	pub, priv, _ := ed25519.GenerateKey(strings.NewReader(seed))
	return pub, priv
}

// qqVerify：签名体 = timestamp + 原始 body，签名头为 hex(Ed25519)。
// 反向代理必须保留原始 body，否则验签必失败。
func qqVerify(pub ed25519.PublicKey, sigHex, ts string, body []byte) bool {
	sig, err := hex.DecodeString(sigHex)
	if err != nil || len(sig) != ed25519.SignatureSize || sig[63]&224 != 0 {
		return false
	}
	msg := make([]byte, 0, len(ts)+len(body))
	msg = append(msg, ts...)
	msg = append(msg, body...)
	return ed25519.Verify(pub, msg, sig)
}

// qqSign：回调地址验证时，用私钥签 event_ts + plain_token，返回 hex。
func qqSign(priv ed25519.PrivateKey, eventTS, plainToken string) string {
	return hex.EncodeToString(ed25519.Sign(priv, []byte(eventTS+plainToken)))
}

func hmacSHA256Hex(secret string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	_, _ = m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

func constEq(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func requireEnv(key string) (string, error) {
	if key == "" {
		return "", errors.New("未配置凭据环境变量名")
	}
	v := getenv(key)
	if v == "" {
		return "", fmt.Errorf("环境变量 %s 未设置（晨曦不把密钥写进配置文件）", key)
	}
	return v, nil
}
