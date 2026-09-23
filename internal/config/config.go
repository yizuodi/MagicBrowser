// Package config 提供系统配置的加载与原子持久化。
// 配置以 JSON 文件形式落盘（写临时文件后 rename），运行期只保留单一结构体在内存。
package config

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Config 是 Magic Browser 的全部可配置项。JSON 标签同时作为管理后台的字段名。
type Config struct {
	AdminPasswordHash string `json:"admin_password_hash,omitempty"`
	AdminPasswordSalt string `json:"admin_password_salt,omitempty"`
	SitePasswordHash  string `json:"site_password_hash,omitempty"`
	SitePasswordSalt  string `json:"site_password_salt,omitempty"` // 空 hash 表示访客免密码

	LLMProviderURL string `json:"llm_provider_url"` // OpenAI 兼容根地址，如 https://api.groq.com/openai/v1
	LLMAPIKey      string `json:"llm_api_key,omitempty"`
	LLMModelName   string `json:"llm_model_name"`

	ContextCompressThreshold int    `json:"context_compress_threshold"` // 触发上下文压缩的 token 阈值
	LoadingText              string `json:"loading_text"`               // 前端加载遮罩文案
	SystemPrompt             string `json:"system_prompt"`
	CDNBaseURL               string `json:"cdn_base_url"` // 生成页面引用的 Tailwind CDN
	GenTimeoutSec            int    `json:"gen_timeout_sec"`

	// InitialAdminPassword 仅在首次生成配置时短暂携带（不落盘 JSON 的语义由
	// persistLocked 前清空保证），供 main 打印与写入提示文件。
	InitialAdminPassword string `json:"-"`
}

// DefaultSystemPrompt 是生成页面的全局系统提示词（后台可改）。
const DefaultSystemPrompt = `你是 Magic Browser 的页面渲染引擎。用户会给你一个网址和一个可选的操作(Action)，你需要即时生成该网址对应的完整 HTML 页面。

规则：
1. 只输出一个完整的 HTML 文档，从 <!DOCTYPE html> 开始到 </html> 结束。不要输出任何解释文字，不要使用 markdown 代码围栏。
2. 在 <head> 中引入 Tailwind：<script src="{{CDN_BASE}}"></script>，样式一律用 Tailwind 类编写，可附加少量内联 <style>。
3. 根据域名词义推断网站类型与内容主题，页面要内容充实、有真实感：标题、导航、卡片、列表、图表占位等布局要丰富。
4. 页面内的轻量交互（展开菜单、切换 Tab、弹窗、勾选等）用内联 vanilla JS 直接实现，不依赖外部库。
5. 深度交互（跳转到新页面）必须用约定的标记：
   - 链接/按钮跳转：<a href="目标路径" data-magic-nav> 或任意元素加 data-magic-nav 与 data-magic-url="目标路径"。
   - 表单提交跳转：<form data-magic-form action="目标路径">。
   - 目标路径写成有语义的相对/绝对 URL（如 /product/42、/checkout）。
   - 不需要深度交互的本地表单加 data-magic-local。
6. 保持与用户此前浏览上下文的连贯性（如果提供了历史页面与操作记录）：同一站点风格一致、已填信息保留、状态延续。
7. 输出语言与网站域名词义匹配（如 .magic/.cn 域名倾向中文内容）。`

// Defaults 返回一份默认配置。
func Defaults() Config {
	return Config{
		LLMProviderURL:           "https://api.groq.com/openai/v1",
		LLMModelName:             "llama-3.3-70b-versatile",
		ContextCompressThreshold: 200000,
		LoadingText:              "豆包正在为你加载页面中~",
		SystemPrompt:             DefaultSystemPrompt,
		CDNBaseURL:               "https://cdn.tailwindcss.com",
		GenTimeoutSec:            120,
	}
}

// Store 持有配置的内存副本与落盘路径，读多写少用 RWMutex 保护。
type Store struct {
	mu   sync.RWMutex
	path string
	cfg  Config
}

// Load 从 path 读取配置；文件不存在时写入默认配置（含随机生成的初始管理密码）并返回。
func Load(path string) (*Store, error) {
	s := &Store{path: path}
	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		cfg := Defaults()
		pw, err := RandomPassword(12)
		if err != nil {
			return nil, fmt.Errorf("generate initial admin password: %w", err)
		}
		if err := cfg.SetAdminPassword(pw); err != nil {
			return nil, err
		}
		cfg.InitialAdminPassword = pw // 见下方字段说明
		s.cfg = cfg
		if err := s.persistLocked(); err != nil {
			return nil, err
		}
		return s, nil
	case err != nil:
		return nil, fmt.Errorf("read config: %w", err)
	}
	if err := json.Unmarshal(raw, &s.cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	// 补齐缺省字段（老配置升级场景）。
	def := Defaults()
	if s.cfg.LoadingText == "" {
		s.cfg.LoadingText = def.LoadingText
	}
	if s.cfg.SystemPrompt == "" {
		s.cfg.SystemPrompt = def.SystemPrompt
	}
	if s.cfg.CDNBaseURL == "" {
		s.cfg.CDNBaseURL = def.CDNBaseURL
	}
	if s.cfg.GenTimeoutSec <= 0 {
		s.cfg.GenTimeoutSec = def.GenTimeoutSec
	}
	if s.cfg.ContextCompressThreshold <= 0 {
		s.cfg.ContextCompressThreshold = def.ContextCompressThreshold
	}
	return s, nil
}

// Get 返回配置的值拷贝（敏感字段由调用方决定是否打码）。
func (s *Store) Get() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

// Update 在锁内对配置副本执行 fn 后原子落盘；fn 返回错误则放弃修改。
func (s *Store) Update(fn func(*Config) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.cfg // 结构体浅拷贝，字段均为值类型
	if err := fn(&next); err != nil {
		return err
	}
	s.cfg = next
	return s.persistLocked()
}

// persistLocked 写临时文件 + rename，保证崩溃安全的原子替换。调用方需持有写锁。
func (s *Store) persistLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}
	raw, err := json.MarshalIndent(s.cfg, "", "  ")
	if err != nil {
		return err
	}
	_ = s.cfg.InitialAdminPassword // 该字段 json:"-"，不会序列化
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("write temp config: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("rename config: %w", err)
	}
	return nil
}

// RandomPassword 生成 n 位含大小写与数字的随机密码，用于首次启动。
func RandomPassword(n int) (string, error) {
	const alphabet = "abcdefghjkmnpqrstuvwxyzABCDEFGHJKMNPQRSTUVWXYZ23456789"
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	for i, b := range buf {
		buf[i] = alphabet[int(b)%len(alphabet)]
	}
	return string(buf), nil
}
