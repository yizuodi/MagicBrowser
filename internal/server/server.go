// Package server 装配 Magic Browser 的全部 HTTP 路由：
// 浏览器前端、页面生成流、访客/管理员鉴权与后台配置。
package server

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"strings"
	"time"

	"magic/internal/auth"
	"magic/internal/config"
	"magic/internal/history"
	"magic/internal/llm"
	"magic/internal/session"
)

//go:embed all:web
var webFS embed.FS

// Server 聚合全部依赖。
type Server struct {
	Cfg      *config.Store
	Tokens   *auth.Tokens
	Sessions *session.Store
	Tickets  *TicketStore
	History  *history.Store // 可为 nil（裸测试场景，handler 会 503）
	Sem      chan struct{}  // 并发生成流信号量
	Log      *log.Logger
}

// New 构造 Server。hist 允许为 nil。
func New(cfg *config.Store, tokens *auth.Tokens, hist *history.Store) *Server {
	return &Server{
		Cfg:      cfg,
		Tokens:   tokens,
		Sessions: session.NewStore(),
		Tickets:  NewTicketStore(),
		History:  hist,
		Sem:      make(chan struct{}, 16),
		Log:      log.New(log.Default().Writer(), "[magic] ", log.Default().Flags()),
	}
}

// StartSweeper 启动会话与 ticket 的周期清扫。
func (s *Server) StartSweeper(stop <-chan struct{}) {
	s.Sessions.StartSweep(stop)
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				s.Tickets.Sweep()
			case <-stop:
				return
			}
		}
	}()
}

// llmClient 依据当前配置构造一个 LLM 客户端。
func (s *Server) llmClient() *llm.Client {
	cfg := s.Cfg.Get()
	return &llm.Client{
		BaseURL: cfg.LLMProviderURL,
		APIKey:  cfg.LLMAPIKey,
		Model:   cfg.LLMModelName,
		HTTP: &http.Client{
			Timeout: time.Duration(cfg.GenTimeoutSec) * time.Second,
		},
	}
}

// Handler 返回根路由。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// 公开端点。
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /api/v1/config/public", s.handlePublicConfig)
	mux.HandleFunc("POST /api/v1/auth/site", s.handleSiteAuth)
	mux.HandleFunc("POST /api/v1/admin/login", s.handleAdminLogin)
	mux.HandleFunc("POST /api/v1/admin/logout", s.handleAdminLogout)

	// 需要访客态的端点。
	mux.Handle("POST /api/v1/browser/navigate", s.requireSite(s.handleNavigate))
	mux.Handle("POST /api/v1/browser/replay", s.requireSite(s.handleReplay))
	mux.Handle("GET /api/v1/page/{ticket}", s.requireSite(s.handlePage))

	// 需要管理员态的端点。
	mux.Handle("GET /api/v1/admin/config", s.requireAdmin(s.handleAdminGetConfig))
	mux.Handle("PUT /api/v1/admin/config", s.requireAdmin(s.handleAdminPutConfig))
	mux.Handle("GET /api/v1/admin/history", s.requireAdmin(s.handleAdminHistoryList))
	mux.Handle("DELETE /api/v1/admin/history", s.requireAdmin(s.handleAdminHistoryClear))
	mux.Handle("DELETE /api/v1/admin/history/{id}", s.requireAdmin(s.handleAdminHistoryDelete))

	// 历史 / 分享（访客态；HTML 骨架公开，数据在 API 层受保护——与 / 同策略）。
	mux.Handle("GET /api/v1/history/{id}/meta", s.requireSite(s.handleHistoryMeta))
	mux.Handle("POST /api/v1/history/{id}/resume", s.requireSite(s.handleHistoryResume))
	mux.Handle("GET /api/v1/snapshot/{id}", s.requireSite(s.handleSnapshot))
	mux.HandleFunc("GET /s/{id}", s.staticHandler("web/share.html"))

	// 静态 UI。
	mux.Handle("GET /", s.requireSitePage(s.staticHandler("web/browser.html")))
	mux.Handle("GET /admin", s.staticHandler("web/admin.html"))
	mux.Handle("GET /browser.js", s.staticHandler("web/browser.js"))
	mux.Handle("GET /browser.css", s.staticHandler("web/browser.css"))
	mux.Handle("GET /admin.js", s.staticHandler("web/admin.js"))

	return logRequests(mux)
}

// logRequests 打一行简洁的访问日志（含状态码，方便排查登录问题）。
func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, code: http.StatusOK}
		next.ServeHTTP(rec, r)
		log.Printf("%s %s %d %s", r.Method, r.URL.Path, rec.code, time.Since(start).Round(time.Millisecond))
	})
}

// statusRecorder 记录 handler 写入的状态码，并透传 Flusher 等可选接口
// （否则包一层后 w.(http.Flusher) 断言失败，流式页面全挂）。
type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.code = code
	r.ResponseWriter.WriteHeader(code)
}

// Flush 透传 http.Flusher。
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap 支持 http.ResponseController 语义（Go 1.20+）。
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// staticHandler 从 embed FS 输出一个静态文件。
func (s *Server) staticHandler(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw, err := webFS.ReadFile(name)
		if err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		switch {
		case strings.HasSuffix(name, ".html"):
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
		case strings.HasSuffix(name, ".js"):
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		case strings.HasSuffix(name, ".css"):
			w.Header().Set("Content-Type", "text/css; charset=utf-8")
		}
		w.Header().Set("Cache-Control", "no-cache")
		w.Write(raw)
	}
}

// requireSite 拦截未通过访客鉴权的 API 请求。
// 未启用访客密码门时自动放行（但仍签发匿名访客态由前端处理，这里直接通过）。
func (s *Server) requireSite(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cfg := s.Cfg.Get(); cfg.SiteGateEnabled() {
			if err := s.Tokens.Authorize(r, auth.PurposeSite); err != nil {
				jsonError(w, http.StatusUnauthorized, "需要访问密码")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// requireSitePage 对页面请求做访客鉴权：通过则放行，否则跳到登录页。
// 登录页由前端渲染（拉取 public config 判断 gate 状态），因此这里总是放行 HTML 本身，
// 真正的数据保护在 API 层。
func (s *Server) requireSitePage(next http.Handler) http.Handler {
	return next // 见上注释：HTML 骨架公开，API 才是边界
}

// requireAdmin 拦截未通过管理员鉴权的请求。
func (s *Server) requireAdmin(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := s.Tokens.Authorize(r, auth.PurposeAdmin); err != nil {
			jsonError(w, http.StatusUnauthorized, "需要管理员登录")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// handleHealth 健康检查。
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, `{"ok":true}`)
}

// randomID 生成 16 字节随机十六进制 ID。
func randomID(prefix string) string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// 退化为纳秒时间戳，保住唯一性。
		return fmt.Sprintf("%s%d", prefix, time.Now().UnixNano())
	}
	return prefix + hex.EncodeToString(b)
}

// jsonError 输出统一 JSON 错误。
func jsonError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	fmt.Fprintf(w, `{"error":%q}`, msg)
}

// webOpen 打开 embed 的 web 子树（供 fs.WalkDir 等场景）。
func webRoot() fs.FS {
	sub, _ := fs.Sub(webFS, "web")
	return sub
}
