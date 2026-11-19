package security

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"

	"golang.org/x/crypto/hkdf"
)

const cipherVersionPrefix = "v1:"

var (
	// ErrMissingEncryptionKey 表示生产模式未配置加密主密钥。
	ErrMissingEncryptionKey = errors.New("未配置 APIKey 加密主密钥（请设置环境变量 ARCHDOC_ENCRYPTION_KEY 或 security.encryption_key）")
	// ErrInvalidCiphertext 表示密文格式损坏或 AES-GCM 认证失败。
	ErrInvalidCiphertext = errors.New("密文格式非法或校验失败")
)

// Cipher 提供基于 AES-256-GCM 的敏感凭据对称加解密能力（密文格式：v1:<base64(nonce || ciphertext || tag)>）。
type Cipher struct {
	aead cipher.AEAD
}

// NewCipher 初始化 AES-256-GCM 加密器。
// rawKey 支持：32 字节原始串、64 位十六进制串或标准 Base64 编码的 32 字节密钥。
// 当 rawKey 为空且 allowDevFallback=true 时，通过 HKDF-SHA256 从 jwtSecretFallback 派生 32 字节开发密钥并打印明显提示。
func NewCipher(rawKey, jwtSecretFallback string, allowDevFallback bool) (*Cipher, error) {
	key, err := resolveMasterKey(rawKey, jwtSecretFallback, allowDevFallback)
	if err != nil {
		return nil, err
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("初始化 AES 密码块失败: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("初始化 AES-GCM 失败: %w", err)
	}
	return &Cipher{aead: aead}, nil
}

func resolveMasterKey(rawKey, jwtSecretFallback string, allowDevFallback bool) ([]byte, error) {
	trimmed := strings.TrimSpace(rawKey)
	if trimmed != "" {
		// 1. 尝试 64 位 Hex 解码 -> 32 字节
		if len(trimmed) == 64 {
			if decoded, err := hex.DecodeString(trimmed); err == nil && len(decoded) == 32 {
				return decoded, nil
			}
		}
		// 2. 尝试 Base64 解码 -> 32 字节
		if decoded, err := base64.StdEncoding.DecodeString(trimmed); err == nil && len(decoded) == 32 {
			return decoded, nil
		}
		if decoded, err := base64.RawStdEncoding.DecodeString(trimmed); err == nil && len(decoded) == 32 {
			return decoded, nil
		}
		// 3. 若恰好为 32 字节原始密钥
		if len(trimmed) == 32 {
			return []byte(trimmed), nil
		}
		return nil, fmt.Errorf("ARCHDOC_ENCRYPTION_KEY 长度不合法：必须解码为 32 字节（支持 64 位 Hex 或 Base64）")
	}

	if !allowDevFallback {
		return nil, ErrMissingEncryptionKey
	}

	seed := strings.TrimSpace(jwtSecretFallback)
	if seed == "" {
		return nil, ErrMissingEncryptionKey
	}

	log.Printf("[Code-Archdoc/Security] WARNING: 未配置 ARCHDOC_ENCRYPTION_KEY，当前处于独立开发调试模式，已通过 HKDF-SHA256 派生开发态加密密钥（生产环境请务必注入独立 32 字节主密钥）")
	r := hkdf.New(sha256.New, []byte(seed), []byte("code-archdoc-dev-salt-v1"), []byte("archdoc-apikey-aes256gcm-v1"))
	derived := make([]byte, 32)
	if _, err := io.ReadFull(r, derived); err != nil {
		return nil, fmt.Errorf("HKDF 派生开发态密钥失败: %w", err)
	}
	return derived, nil
}

// Encrypt 使用随机 12 字节 Nonce 对明文执行 AES-256-GCM 认证加密。
func (c *Cipher) Encrypt(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("生成加密随机数失败: %w", err)
	}
	sealed := c.aead.Seal(nonce, nonce, []byte(plaintext), nil)
	return cipherVersionPrefix + base64.StdEncoding.EncodeToString(sealed), nil
}

// Decrypt 解密并验证 v1:<base64> 格式密文。
func (c *Cipher) Decrypt(ciphertext string) (string, error) {
	if ciphertext == "" {
		return "", nil
	}
	if !strings.HasPrefix(ciphertext, cipherVersionPrefix) {
		return "", ErrInvalidCiphertext
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(ciphertext, cipherVersionPrefix))
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidCiphertext, err)
	}
	nonceSize := c.aead.NonceSize()
	if len(raw) < nonceSize+c.aead.Overhead() {
		return "", ErrInvalidCiphertext
	}
	nonce, body := raw[:nonceSize], raw[nonceSize:]
	plain, err := c.aead.Open(nil, nonce, body, nil)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidCiphertext, err)
	}
	return string(plain), nil
}
