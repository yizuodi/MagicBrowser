package server

import (
	"encoding/json"
	"io"
	"net/http"

	"magic/internal/auth"
	"magic/internal/config"
)

// handleSiteAuth 访客密码换 cookie。未启用密码门时直接放行发 cookie。
func (s *Server) handleSiteAuth(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad json")
		return
	}
	cfg := s.Cfg.Get()
	if !cfg.VerifySitePassword(req.Password) {
		jsonError(w, http.StatusUnauthorized, "访问密码不正确")
		return
	}
	s.Tokens.IssueCookie(w, auth.PurposeSite)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Write([]byte(`{"ok":true}`))
}

// handleAdminLogin 管理员密码换 cookie。
func (s *Server) handleAdminLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad json")
		return
	}
	cfg := s.Cfg.Get()
	if !cfg.VerifyAdminPassword(req.Password) {
		jsonError(w, http.StatusUnauthorized, "管理员密码不正确")
		return
	}
	s.Tokens.IssueCookie(w, auth.PurposeAdmin)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Write([]byte(`{"ok":true}`))
}

// handleAdminLogout 清除管理员 cookie。
func (s *Server) handleAdminLogout(w http.ResponseWriter, r *http.Request) {
	auth.ClearCookie(w, auth.PurposeAdmin)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Write([]byte(`{"ok":true}`))
}

// handlePublicConfig 暴露前端需要的非敏感配置。
func (s *Server) handlePublicConfig(w http.ResponseWriter, r *http.Request) {
	cfg := s.Cfg.Get()
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(map[string]any{
		"loading_text": cfg.LoadingText,
		"gate_enabled": cfg.SiteGateEnabled(),
		"site_name":    "Magic Browser",
	})
}

// adminConfigView 是后台 GET 返回的配置视图（api_key 打码）。
type adminConfigView struct {
	LLMProviderURL           string `json:"llm_provider_url"`
	LLMModelName             string `json:"llm_model_name"`
	HasAPIKey                bool   `json:"has_api_key"`
	ContextCompressThreshold int    `json:"context_compress_threshold"`
	LoadingText              string `json:"loading_text"`
	SystemPrompt             string `json:"system_prompt"`
	CDNBaseURL               string `json:"cdn_base_url"`
	GenTimeoutSec            int    `json:"gen_timeout_sec"`
	SiteGateEnabled          bool   `json:"site_gate_enabled"`
}

// handleAdminGetConfig 返回打码后的配置。
func (s *Server) handleAdminGetConfig(w http.ResponseWriter, r *http.Request) {
	cfg := s.Cfg.Get()
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(adminConfigView{
		LLMProviderURL:           cfg.LLMProviderURL,
		LLMModelName:             cfg.LLMModelName,
		HasAPIKey:                cfg.LLMAPIKey != "",
		ContextCompressThreshold: cfg.ContextCompressThreshold,
		LoadingText:              cfg.LoadingText,
		SystemPrompt:             cfg.SystemPrompt,
		CDNBaseURL:               cfg.CDNBaseURL,
		GenTimeoutSec:            cfg.GenTimeoutSec,
		SiteGateEnabled:          cfg.SiteGateEnabled(),
	})
}

// adminConfigUpdate 是后台 PUT 的请求体。指针字段区分「未提供」与「显式清空」。
type adminConfigUpdate struct {
	LLMProviderURL           *string `json:"llm_provider_url"`
	LLMAPIKey                *string `json:"llm_api_key"` // 空串=保留原值
	LLMModelName             *string `json:"llm_model_name"`
	ContextCompressThreshold *int    `json:"context_compress_threshold"`
	LoadingText              *string `json:"loading_text"`
	SystemPrompt             *string `json:"system_prompt"`
	CDNBaseURL               *string `json:"cdn_base_url"`
	GenTimeoutSec            *int    `json:"gen_timeout_sec"`
	AdminPassword            *string `json:"admin_password"` // 空串=保留
	SitePassword             *string `json:"site_password"`  // 空串=保留；"__disable__"=关闭密码门
	DisableSiteGate          *bool   `json:"disable_site_gate"`
}

// handleAdminPutConfig 部分更新配置。
func (s *Server) handleAdminPutConfig(w http.ResponseWriter, r *http.Request) {
	var req adminConfigUpdate
	if err := json.NewDecoder(io.LimitReader(r.Body, 256<<10)).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad json: "+err.Error())
		return
	}
	err := s.Cfg.Update(func(c *config.Config) error { return applyUpdate(c, &req) })
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "保存失败: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Write([]byte(`{"ok":true}`))
}

// applyUpdate 把请求体合并进配置。密钥/密码类字段空串=保留原值。
func applyUpdate(c *config.Config, req *adminConfigUpdate) error {
	if req.LLMProviderURL != nil && *req.LLMProviderURL != "" {
		c.LLMProviderURL = *req.LLMProviderURL
	}
	if req.LLMAPIKey != nil && *req.LLMAPIKey != "" {
		c.LLMAPIKey = *req.LLMAPIKey
	}
	if req.LLMModelName != nil && *req.LLMModelName != "" {
		c.LLMModelName = *req.LLMModelName
	}
	if req.ContextCompressThreshold != nil && *req.ContextCompressThreshold > 0 {
		c.ContextCompressThreshold = *req.ContextCompressThreshold
	}
	if req.LoadingText != nil && *req.LoadingText != "" {
		c.LoadingText = *req.LoadingText
	}
	if req.SystemPrompt != nil && *req.SystemPrompt != "" {
		c.SystemPrompt = *req.SystemPrompt
	}
	if req.CDNBaseURL != nil && *req.CDNBaseURL != "" {
		c.CDNBaseURL = *req.CDNBaseURL
	}
	if req.GenTimeoutSec != nil && *req.GenTimeoutSec > 0 {
		c.GenTimeoutSec = *req.GenTimeoutSec
	}
	if req.AdminPassword != nil && *req.AdminPassword != "" {
		if err := c.SetAdminPassword(*req.AdminPassword); err != nil {
			return err
		}
	}
	if req.DisableSiteGate != nil && *req.DisableSiteGate {
		if err := c.SetSitePassword(""); err != nil {
			return err
		}
	} else if req.SitePassword != nil && *req.SitePassword != "" {
		if err := c.SetSitePassword(*req.SitePassword); err != nil {
			return err
		}
	}
	return nil
}
