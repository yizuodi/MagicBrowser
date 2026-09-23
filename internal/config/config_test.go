package config

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadCreatesDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	s, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cfg := s.Get()
	if cfg.LoadingText == "" || cfg.SystemPrompt == "" || cfg.ContextCompressThreshold != 200000 {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	if cfg.AdminPasswordHash == "" || cfg.AdminPasswordSalt == "" {
		t.Fatal("initial admin password hash not set")
	}
	if cfg.InitialAdminPassword == "" || len(cfg.InitialAdminPassword) != 12 {
		t.Fatalf("initial admin password not surfaced: %q", cfg.InitialAdminPassword)
	}
}

func TestLoadPersistAndReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	s, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	err = s.Update(func(c *Config) error {
		c.LoadingText = "自定义加载文案~"
		return c.SetSitePassword("visitor-pw")
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	s2, err := Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	cfg := s2.Get()
	if cfg.LoadingText != "自定义加载文案~" {
		t.Fatalf("loading text not persisted: %q", cfg.LoadingText)
	}
	if !cfg.SiteGateEnabled() {
		t.Fatal("site gate should be enabled after reload")
	}
	if !cfg.VerifySitePassword("visitor-pw") {
		t.Fatal("site password should verify after reload")
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), "visitor-pw") {
		t.Fatal("plaintext password leaked into JSON")
	}
}

func TestUpdateRollbackOnError(t *testing.T) {
	dir := t.TempDir()
	s, err := Load(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	boom := errUpdate
	err = s.Update(func(c *Config) error {
		c.LoadingText = "should not persist"
		return boom
	})
	if err != errUpdate {
		t.Fatalf("expected error, got %v", err)
	}
	if s.Get().LoadingText == "should not persist" {
		t.Fatal("config mutated despite fn error")
	}
}

var errUpdate = &simpleErr{}

type simpleErr struct{}

func (*simpleErr) Error() string { return "boom" }

func TestPasswordRoundTrip(t *testing.T) {
	var c Config
	if err := c.SetAdminPassword("s3cret"); err != nil {
		t.Fatalf("SetAdminPassword: %v", err)
	}
	if !c.VerifyAdminPassword("s3cret") {
		t.Fatal("correct password rejected")
	}
	if c.VerifyAdminPassword("wrong") {
		t.Fatal("wrong password accepted")
	}
	// 重新设置密码应换盐，旧哈希失效。
	oldHash := c.AdminPasswordHash
	_ = c.SetAdminPassword("s3cret")
	if c.AdminPasswordHash == oldHash {
		t.Fatal("salt not rotated on password reset")
	}
}
