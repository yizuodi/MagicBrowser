package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
)

// historyDisabled 历史存储未初始化时的统一响应。
func historyDisabled(w http.ResponseWriter) {
	jsonError(w, http.StatusServiceUnavailable, "历史存储未启用")
}

// handleAdminHistoryList GET /api/v1/admin/history?limit=&offset= → {items,total}（新→旧）。
func (s *Server) handleAdminHistoryList(w http.ResponseWriter, r *http.Request) {
	if s.History == nil {
		historyDisabled(w)
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	items, total := s.History.List(limit, offset)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(map[string]any{"items": items, "total": total})
}

// handleAdminHistoryDelete DELETE /api/v1/admin/history/{id} → 单条删除。
func (s *Server) handleAdminHistoryDelete(w http.ResponseWriter, r *http.Request) {
	if s.History == nil {
		historyDisabled(w)
		return
	}
	id := r.PathValue("id")
	if !s.History.Delete(id) {
		jsonError(w, http.StatusNotFound, "历史条目不存在")
		return
	}
	s.Log.Printf("历史已删除: %s", id)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Write([]byte(`{"ok":true}`))
}

// handleAdminHistoryClear DELETE /api/v1/admin/history → 清空全部。
func (s *Server) handleAdminHistoryClear(w http.ResponseWriter, r *http.Request) {
	if s.History == nil {
		historyDisabled(w)
		return
	}
	if err := s.History.Clear(); err != nil {
		jsonError(w, http.StatusInternalServerError, "清空失败: "+err.Error())
		return
	}
	s.Log.Printf("历史已全部清空")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Write([]byte(`{"ok":true}`))
}

// handleHistoryMeta GET /api/v1/history/{id}/meta → 元数据。
// 兼作分享页的 gate 探针：401=需密码，404=快照不存在，200=放行。
func (s *Server) handleHistoryMeta(w http.ResponseWriter, r *http.Request) {
	if s.History == nil {
		historyDisabled(w)
		return
	}
	m, _, ok := s.History.Get(r.PathValue("id"))
	if !ok {
		jsonError(w, http.StatusNotFound, "历史页面不存在")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(m)
}

// handleHistoryResume POST /api/v1/history/{id}/resume
// 新建会话并把该页作为「上一页」种子 → {session_id, url}。
// 前端拿到后走正常 navigateTo，LLM 上下文自动连贯。
func (s *Server) handleHistoryResume(w http.ResponseWriter, r *http.Request) {
	if s.History == nil {
		historyDisabled(w)
		return
	}
	m, html, ok := s.History.Get(r.PathValue("id"))
	if !ok {
		jsonError(w, http.StatusNotFound, "历史页面不存在")
		return
	}
	sess := s.Sessions.GetOrCreate(randomID("sess_"))
	sess.RecordPage(m.URL, html) // 种子页：BuildMessages 会作为「上一页」
	sess.Touch()
	s.Log.Printf("session=%s 从历史 %s 恢复（url=%s）", sess.ID, m.ID, m.URL)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(map[string]string{"session_id": sess.ID, "url": m.URL})
}

// handleSnapshot GET /api/v1/snapshot/{id} → bridgeTag + 存储 HTML。
// 作为分享页 iframe 的 src，可反复访问（非一次性 ticket）。
func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	if s.History == nil {
		historyDisabled(w)
		return
	}
	_, html, ok := s.History.Get(r.PathValue("id"))
	if !ok {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `<!DOCTYPE html><html><head><meta charset="utf-8"><style>body{font-family:system-ui,sans-serif;display:flex;align-items:center;justify-content:center;height:100vh;color:#646a73;background:#f5f6f8}</style></head><body><div>快照不存在或已被删除</div></body></html>`)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	io.WriteString(w, bridgeTag) // 深度交互仍走 postMessage 闭环
	io.WriteString(w, html)
}
