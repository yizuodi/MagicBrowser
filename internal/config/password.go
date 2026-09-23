package config

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
)

// hashPassword 用随机盐做单次 SHA-256。选择快哈希是零依赖权衡：
// 对局域网玩具场景够用；若威胁模型升级，替换为 golang.org/x/crypto/bcrypt。
func hashPassword(password, salt string) string {
	sum := sha256.Sum256([]byte(salt + ":" + password))
	return hex.EncodeToString(sum[:])
}

// newSalt 生成 16 字节随机盐的十六进制表示。
func newSalt() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// verifyPassword 恒时比较 (salt+password) 的哈希与 storedHash。
func verifyPassword(password, salt, storedHash string) bool {
	if salt == "" || storedHash == "" {
		return false
	}
	got := hashPassword(password, salt)
	return subtle.ConstantTimeCompare([]byte(got), []byte(storedHash)) == 1
}

// SetAdminPassword 为 cfg 设置管理员密码哈希与配套盐。
func (c *Config) SetAdminPassword(pw string) error {
	salt, err := newSalt()
	if err != nil {
		return err
	}
	c.AdminPasswordSalt = salt
	c.AdminPasswordHash = hashPassword(pw, salt)
	return nil
}

// VerifyAdminPassword 校验管理员密码。
func (c *Config) VerifyAdminPassword(pw string) bool {
	return verifyPassword(pw, c.AdminPasswordSalt, c.AdminPasswordHash)
}

// SetSitePassword 设置访客访问密码；空字符串表示关闭访客密码门。
func (c *Config) SetSitePassword(pw string) error {
	if pw == "" {
		c.SitePasswordSalt = ""
		c.SitePasswordHash = ""
		return nil
	}
	salt, err := newSalt()
	if err != nil {
		return err
	}
	c.SitePasswordSalt = salt
	c.SitePasswordHash = hashPassword(pw, salt)
	return nil
}

// VerifySitePassword 校验访客密码；未启用密码门时对任意输入返回 true。
func (c *Config) VerifySitePassword(pw string) bool {
	if !c.SiteGateEnabled() {
		return true
	}
	return verifyPassword(pw, c.SitePasswordSalt, c.SitePasswordHash)
}

// SiteGateEnabled 返回是否启用了访客访问密码。
func (c *Config) SiteGateEnabled() bool {
	return c.SitePasswordHash != ""
}
