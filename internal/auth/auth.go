// Package auth 提供基于 HMAC 签名的无状态 token 与 HttpOnly cookie 助手。
// token 形如 base64url(purpose|expiryUnix)|base64url(HMAC-SHA256(secret, payload))，
// 服务端无需为登录态开辟任何存储。
package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Purpose 区分访客态与管理员态，防止 token 跨用途复用。
type Purpose string

const (
	PurposeSite  Purpose = "site"
	PurposeAdmin Purpose = "admin"
)

const (
	cookieSite  = "magic_site"
	cookieAdmin = "magic_admin"

	siteTTL  = 12 * time.Hour
	adminTTL = 12 * time.Hour
)

// Tokens 持有签名密钥，完成 token 的签发与校验。
type Tokens struct{ secret []byte }

// NewTokens 用 secret 构造（secret 长度应 >= 32 字节）。
func NewTokens(secret []byte) *Tokens { return &Tokens{secret: secret} }

// Sign 为 purpose 签发一个 ttl 有效期的 token。
func (t *Tokens) Sign(purpose Purpose, ttl time.Duration) (string, time.Time) {
	exp := time.Now().Add(ttl)
	payload := fmt.Sprintf("%s|%d", purpose, exp.Unix())
	mac := hmac.New(sha256.New, t.secret)
	mac.Write([]byte(payload))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + sig, exp
}

// Verify 校验 token：签名恒时比较 + 用途匹配 + 未过期。
func (t *Tokens) Verify(token string, purpose Purpose) bool {
	dot := strings.LastIndexByte(token, '.')
	if dot <= 0 {
		return false
	}
	rawPayload, err := base64.RawURLEncoding.DecodeString(token[:dot])
	if err != nil {
		return false
	}
	sig, err := base64.RawURLEncoding.DecodeString(token[dot+1:])
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, t.secret)
	mac.Write(rawPayload)
	if subtle.ConstantTimeCompare(sig, mac.Sum(nil)) != 1 {
		return false
	}
	parts := strings.SplitN(string(rawPayload), "|", 2)
	if len(parts) != 2 || parts[0] != string(purpose) {
		return false
	}
	expUnix, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || time.Now().Unix() > expUnix {
		return false
	}
	return true
}

// ErrNoAuth 表示请求未携带有效凭证。
var ErrNoAuth = errors.New("auth: missing or invalid token")

// Authorize 从请求 cookie 中校验指定用途的 token。
func (t *Tokens) Authorize(r *http.Request, purpose Purpose) error {
	name := cookieSite
	if purpose == PurposeAdmin {
		name = cookieAdmin
	}
	ck, err := r.Cookie(name)
	if err != nil || !t.Verify(ck.Value, purpose) {
		return ErrNoAuth
	}
	return nil
}

// IssueCookie 把新签发的 token 写入响应的 HttpOnly cookie。
func (t *Tokens) IssueCookie(w http.ResponseWriter, purpose Purpose) error {
	var (
		ttl  time.Duration
		name string
	)
	switch purpose {
	case PurposeAdmin:
		name, ttl = cookieAdmin, adminTTL
	default:
		name, ttl = cookieSite, siteTTL
	}
	token, exp := t.Sign(purpose, ttl)
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    token,
		Path:     "/",
		Expires:  exp,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	return nil
}

// ClearCookie 使指定用途的 cookie 失效（用于登出）。
func ClearCookie(w http.ResponseWriter, purpose Purpose) {
	name := cookieSite
	if purpose == PurposeAdmin {
		name = cookieAdmin
	}
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}
