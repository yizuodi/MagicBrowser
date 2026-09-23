package server

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"magic/internal/llm"
	"magic/internal/session"
)

// actionPayload 是 navigate 请求里的用户操作。
type actionPayload struct {
	Type    string            `json:"type"`
	URL     string            `json:"url"`
	Element string            `json:"element"`
	Form    map[string]string `json:"form"`
}

// navigateRequest 是 POST /api/v1/browser/navigate 的请求体。
type navigateRequest struct {
	SessionID string         `json:"session_id"`
	URL       string         `json:"url"`
	Action    *actionPayload `json:"action"`
}

// navigateResponse 是 navigate 的响应体。
type navigateResponse struct {
	SessionID string    `json:"session_id"`
	StreamURL string    `json:"stream_url"`
	ExpiresAt time.Time `json:"expires_at"`
	PageIndex int       `json:"page_index"` // 本次生成结果将落到的页下标（replay 用）
	HistoryID string    `json:"history_id"` // 预分配的历史 id（生成完成后可分享；失败则失效）
}

// handleNavigate 是整个系统的入口：鉴权 → 压缩检查 → 建 ticket。
func (s *Server) handleNavigate(w http.ResponseWriter, r *http.Request) {
	var req navigateRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "请求体不是合法 JSON: "+err.Error())
		return
	}
	url := session.CleanURL(req.URL)
	if url == "" {
		jsonError(w, http.StatusBadRequest, "url 不能为空")
		return
	}
	sessID := req.SessionID
	if sessID == "" {
		sessID = randomID("sess_")
	}
	sess := s.Sessions.GetOrCreate(sessID)

	// 记录操作（无 action 表示首次访问，不记）。
	if req.Action != nil {
		sess.RecordAction(session.Action{
			Type: req.Action.Type, URL: req.Action.URL,
			Element: req.Action.Element, Form: req.Action.Form,
		})
	}

	// 200k 上下文自动压缩（含本次 url+action 的估算）。
	cfg := s.Cfg.Get()
	if est := sess.EstimateTokens() + session.EstimateTokens(url) + inflightEstimate(req.Action); est >= cfg.ContextCompressThreshold {
		s.compressSession(r.Context(), sess, cfg.ContextCompressThreshold)
	}

	ticketID := randomID("t_")
	// 预分配历史 id：navigate 响应即可返回，前端「分享当前页」直接可用；
	// 生成失败不会落盘，id 自然指向不存在的快照（分享端 404）。
	histID := ""
	if s.History != nil {
		histID = randomID("pg_")
	}
	s.Tickets.Create(ticketID, sessID, url, req.Action, -1, histID)
	// 预估页下标 = 当前页数（0 基）。
	resp := navigateResponse{
		SessionID: sessID,
		StreamURL: "/api/v1/page/" + ticketID,
		ExpiresAt: time.Now().Add(ticketTTL),
		PageIndex: sess.PageCount(), // 生成完成后追加
		HistoryID: histID,
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(resp)
}

// inflightEstimate 估算本次 action 附加内容的 token 量。
func inflightEstimate(a *actionPayload) int {
	if a == nil {
		return 0
	}
	n := len(a.URL) + len(a.Element)
	for k, v := range a.Form {
		n += len(k) + len(v)
	}
	return session.EstimateTokens(strings.Repeat("a", n))
}

// compressSession 触发上下文压缩：LLM 非流式摘要 + 剪枝。失败降级硬截断。
func (s *Server) compressSession(ctx context.Context, sess *session.Session, threshold int) {
	started := time.Now()
	snap := sess.Snapshot()
	var b strings.Builder
	b.WriteString("你是浏览会话的状态压缩器。把以下浏览历史压缩成一段 800~1000 token 的状态摘要，" +
		"保留：网站类型与风格约定、用户身份/偏好（如已登录角色）、浏览路径、购物车/表单等暂存数据、当前页面目的。直接输出摘要文本。\n\n")
	if snap.Compressed && snap.Summary != "" {
		b.WriteString("既有摘要（在此基础上增量合并）:\n" + snap.Summary + "\n\n")
	}
	b.WriteString("浏览路径与操作:\n")
	for _, p := range snap.Pages {
		fmt.Fprintf(&b, "- 页面 %s（HTML 尾部片段: %s）\n", p.URL, tail(p.HTML, 400))
	}
	for _, a := range snap.Actions {
		fmt.Fprintf(&b, "- 操作 %s -> %s\n", a.Type, a.URL)
	}

	client := s.llmClient()
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	summary, err := client.Chat(ctx, []llm.Message{
		{Role: "user", Content: b.String()},
	}, 1200)
	if err != nil {
		s.Log.Printf("session=%s 压缩失败（降级硬截断）: %v", sess.ID, err)
		sess.HardTruncate(threshold)
		return
	}
	before := sess.EstimateTokens()
	sess.CompressTo(llm.StripTrailingFence(summary))
	s.Log.Printf("session=%s 已压缩 %d -> %d tokens（耗时 %s）",
		sess.ID, before, sess.EstimateTokens(), time.Since(started).Round(time.Millisecond))
}

// tail 返回字符串尾部 n 字节（按 UTF-8 安全截断可忽略，容错展示用）。
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// handleReplay 重放已生成的页面（前进/后退），不调 LLM。
func (s *Server) handleReplay(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SessionID string `json:"session_id"`
		Index     int    `json:"index"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad json")
		return
	}
	sess := s.Sessions.GetOrCreate(req.SessionID)
	if _, ok := sess.PageAt(req.Index); !ok {
		jsonError(w, http.StatusNotFound, "页面索引不存在")
		return
	}
	ticketID := randomID("t_")
	s.Tickets.Create(ticketID, req.SessionID, "", nil, req.Index, "")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(navigateResponse{
		SessionID: req.SessionID,
		StreamURL: "/api/v1/page/" + ticketID,
		ExpiresAt: time.Now().Add(ticketTTL),
		PageIndex: req.Index,
	})
}

// bridgeTag 是注入到每个生成页面最前方的导航拦截脚本。
// sandbox iframe（无 allow-same-origin）内的页面只能通过 postMessage 与父页通信。
var bridgeTag = "<script>" + bridgeJS + "</script>"

//go:embed web/bridge.js
var bridgeJS string

// handlePage 消费 ticket 并流式输出页面：bridge 脚本 → LLM 增量。
func (s *Server) handlePage(w http.ResponseWriter, r *http.Request) {
	ticketID := r.PathValue("ticket")
	tk, ok := s.Tickets.Consume(ticketID)
	if !ok {
		jsonError(w, http.StatusGone, "ticket 已使用或过期")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		jsonError(w, http.StatusInternalServerError, "流式响应不可用")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	// 重放路径：直接吐已存 HTML。
	if tk.ReplayOf >= 0 {
		sess := s.Sessions.GetOrCreate(tk.SessionID)
		if p, ok := sess.PageAt(tk.ReplayOf); ok {
			io.WriteString(w, bridgeTag)
			io.WriteString(w, p.HTML)
			flusher.Flush()
		}
		return
	}

	sess := s.Sessions.GetOrCreate(tk.SessionID)
	cfg := s.Cfg.Get()

	// 并发生成信号量：客户端断开时必须归还。
	s.Sem <- struct{}{}
	defer func() { <-s.Sem }()

	io.WriteString(w, bridgeTag)
	flusher.Flush()

	// 组装消息。
	sys := llm.BuildSystemPrompt(cfg.SystemPrompt, cfg.CDNBaseURL)
	var act *session.Action
	if tk.Act != nil {
		act = &session.Action{Type: tk.Act.Type, URL: tk.Act.URL, Element: tk.Act.Element, Form: tk.Act.Form}
	}
	msgs := convertMsgs(sess.BuildMessages(session.BuildInput{
		SystemPrompt: sys, URL: tk.URL, Action: act,
	}))

	// 流泵：LLM 增量 → 围栏剥离 → 合并 flush（30ms 或 8KB）→ tee 进累积器。
	var (
		mu             sync.Mutex
		acc            []byte
		stripper       = llm.NewFenceStripper()
		pending        []byte
		lastFlush      = time.Now()
		genCtx, cancel = context.WithTimeout(r.Context(), time.Duration(cfg.GenTimeoutSec)*time.Second)
	)
	defer cancel()

	writeChunk := func(p []byte) {
		if len(p) == 0 {
			return
		}
		pending = append(pending, p...)
		mu.Lock()
		acc = append(acc, p...)
		mu.Unlock()
		if len(pending) >= 8<<10 || time.Since(lastFlush) >= 30*time.Millisecond {
			w.Write(pending)
			flusher.Flush()
			pending = pending[:0]
			lastFlush = time.Now()
		}
	}

	err := s.llmClient().StreamChat(genCtx, msgs, func(delta string) error {
		writeChunk([]byte(stripper.Feed(delta)))
		return nil
	})
	// 冲出围栏剥离器扣留的残留与未 flush 的尾巴。
	writeChunk([]byte(stripper.Finish()))
	if len(pending) > 0 {
		w.Write(pending)
		flusher.Flush()
	}
	if err != nil {
		// 尽力在流内输出错误卡片（可能客户端已断开，忽略写错误）。
		if !errors.Is(err, context.Canceled) {
			reason := err.Error()
			if len(reason) > 500 {
				reason = reason[:500]
			}
			io.WriteString(w, errorCard(reason))
			flusher.Flush()
		}
		s.Log.Printf("session=%s url=%s 生成中断: %v", tk.SessionID, tk.URL, err)
		return
	}

	mu.Lock()
	html := string(acc)
	mu.Unlock()
	sess.RecordPage(tk.URL, llm.StripTrailingFence(html))
	sess.Touch()
	// 持久化历史：失败只记日志，不影响已完成的流。沿用 ticket 预分配的 id。
	if s.History != nil && tk.HistoryID != "" {
		if _, err := s.History.AppendWithID(tk.HistoryID, tk.SessionID, tk.URL, html); err != nil {
			s.Log.Printf("session=%s url=%s 历史落盘失败: %v", tk.SessionID, tk.URL, err)
		}
	}
	s.Log.Printf("session=%s url=%s 生成完成 %d bytes", tk.SessionID, tk.URL, len(html))
}

// errorCard 是流内错误提示（内联样式，不依赖外部资源）。
func errorCard(reason string) string {
	return `<!DOCTYPE html><html><head><meta charset="utf-8"><style>
body{font-family:system-ui,sans-serif;background:#fef2f2;display:flex;align-items:center;justify-content:center;height:100vh;margin:0}
.card{max-width:560px;padding:32px;background:#fff;border:1px solid #fecaca;border-radius:12px;box-shadow:0 4px 12px rgba(0,0,0,.06)}
h1{font-size:18px;color:#b91c1c;margin:0 0 12px}p{color:#7f1d1d;font-size:14px;line-height:1.6;word-break:break-all}
</style></head><body><div class="card"><h1>页面生成失败</h1><p>` + reason + `</p>
<p style="color:#991b1b;font-size:13px">请在地址栏重试，或联系管理员检查后台模型配置。</p></div></body></html>`
}

// convertMsgs 把 session.Msg 转成 llm.Message。
func convertMsgs(ms []session.Msg) []llm.Message {
	out := make([]llm.Message, len(ms))
	for i, m := range ms {
		out[i] = llm.Message{Role: m.Role, Content: m.Content}
	}
	return out
}
