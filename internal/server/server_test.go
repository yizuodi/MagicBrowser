package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"magic/internal/auth"
	"magic/internal/config"
	"magic/internal/history"
)

func newTestServer(t *testing.T) (*Server, func()) {
	t.Helper()
	dir := t.TempDir()
	cfgStore, err := config.Load(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	secret := []byte("01234567890123456789012345678901")
	tokens := auth.NewTokens(secret)
	hist, err := history.Open(filepath.Join(dir, "pages.jsonl"), 50)
	if err != nil {
		t.Fatalf("history.Open: %v", err)
	}
	srv := New(cfgStore, tokens, hist)
	cleanup := func() {
		hist.Close()
	}
	return srv, cleanup
}

func TestServer_HealthAndPublicConfig(t *testing.T) {
	srv, cleanup := newTestServer(t)
	defer cleanup()

	handler := srv.Handler()

	// 1. /healthz
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("/healthz returned %d", rec.Code)
	}
	if strings.TrimSpace(rec.Body.String()) != `{"ok":true}` {
		t.Errorf("unexpected body: %s", rec.Body.String())
	}

	// 2. /api/v1/config/public
	req = httptest.NewRequest(http.MethodGet, "/api/v1/config/public", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("/api/v1/config/public returned %d", rec.Code)
	}
	var pub map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &pub); err != nil {
		t.Fatalf("failed to decode public config: %v", err)
	}
	if pub["site_name"] != "Magic Browser" {
		t.Errorf("expected site_name 'Magic Browser', got %v", pub["site_name"])
	}
}

func TestServer_AdminLoginAndConfig(t *testing.T) {
	srv, cleanup := newTestServer(t)
	defer cleanup()

	handler := srv.Handler()
	initPw := srv.Cfg.Get().InitialAdminPassword

	// 1. Wrong password login
	badLoginReq := httptest.NewRequest(http.MethodPost, "/api/v1/admin/login",
		strings.NewReader(`{"password":"wrong-password"}`))
	badLoginRec := httptest.NewRecorder()
	handler.ServeHTTP(badLoginRec, badLoginReq)
	if badLoginRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for bad password, got %d", badLoginRec.Code)
	}

	// 2. Correct password login
	loginReq := httptest.NewRequest(http.MethodPost, "/api/v1/admin/login",
		strings.NewReader(`{"password":"`+initPw+`"}`))
	loginRec := httptest.NewRecorder()
	handler.ServeHTTP(loginRec, loginReq)
	if loginRec.Code != http.StatusOK {
		t.Fatalf("expected 200 for correct login, got %d", loginRec.Code)
	}

	// Extract cookie
	cookies := loginRec.Result().Cookies()
	var adminCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == "magic_admin" {
			adminCookie = c
			break
		}
	}
	if adminCookie == nil {
		t.Fatalf("magic_admin cookie not set")
	}

	// 3. Unauthorized access to /api/v1/admin/config
	noAuthReq := httptest.NewRequest(http.MethodGet, "/api/v1/admin/config", nil)
	noAuthRec := httptest.NewRecorder()
	handler.ServeHTTP(noAuthRec, noAuthReq)
	if noAuthRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without cookie, got %d", noAuthRec.Code)
	}

	// 4. Authorized access with cookie
	authReq := httptest.NewRequest(http.MethodGet, "/api/v1/admin/config", nil)
	authReq.AddCookie(adminCookie)
	authRec := httptest.NewRecorder()
	handler.ServeHTTP(authRec, authReq)
	if authRec.Code != http.StatusOK {
		t.Fatalf("expected 200 with cookie, got %d", authRec.Code)
	}

	var cfgView adminConfigView
	if err := json.Unmarshal(authRec.Body.Bytes(), &cfgView); err != nil {
		t.Fatalf("unmarshal admin config: %v", err)
	}
	if cfgView.LLMModelName == "" {
		t.Errorf("empty model name in config view")
	}
}

func TestServer_NavigateRequiresValidURL(t *testing.T) {
	srv, cleanup := newTestServer(t)
	defer cleanup()

	handler := srv.Handler()

	// Empty URL should return 400
	req := httptest.NewRequest(http.MethodPost, "/api/v1/browser/navigate",
		strings.NewReader(`{"url":""}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty url, got %d", rec.Code)
	}

	// Valid URL should return ticket stream_url
	req = httptest.NewRequest(http.MethodPost, "/api/v1/browser/navigate",
		strings.NewReader(`{"url":"demo.magic"}`))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for valid url, got %d", rec.Code)
	}

	var resp navigateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal navigateResponse: %v", err)
	}
	if !strings.HasPrefix(resp.StreamURL, "/api/v1/page/t_") {
		t.Errorf("unexpected StreamURL: %s", resp.StreamURL)
	}
	if resp.SessionID == "" {
		t.Errorf("empty SessionID")
	}
}
