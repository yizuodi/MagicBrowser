// Package session 维护生成会话的状态：页面历史、操作记录、压缩摘要，
// 并提供建构 LLM 消息序列与 token 估算。所有会话驻留内存，
// 通过 TTL 清扫与容量上限约束内存占用（<50MB 目标的一部分）。
package session

import (
	"strings"
	"sync"
	"time"
)

// Action 是一次用户深度交互的描述。
type Action struct {
	Type    string            `json:"type"`              // click | link | submit
	URL     string            `json:"url,omitempty"`     // 目标路径
	Element string            `json:"element,omitempty"` // 触发元素描述（截断）
	Form    map[string]string `json:"form,omitempty"`    // 表单数据（截断）
}

// PageRef 是历史中的一页。
type PageRef struct {
	URL  string
	HTML string // 保留尾部（超过单页上限时截头）
}

// 内存约束常量。
const (
	maxSessions   = 64        // 会话总数上限
	maxPages      = 6         // 每会话保留页面数
	maxPageBytes  = 128 << 10 // 单页 HTML 上限（128KB，保尾）
	maxTotalBytes = 384 << 10 // 页面总上限
	maxSummary    = 8 << 10   // 摘要上限
	maxActions    = 200       // 操作记录上限
	sessionTTL    = 30 * time.Minute
	sweepInterval = time.Minute
	elementMax    = 64  // element 描述截断
	formValueMax  = 200 // 表单值截断
)

// Session 是一个生成会话。零值可用，锁由使用方长期持有或按操作持有。
type Session struct {
	ID        string
	mu        sync.Mutex
	CreatedAt time.Time
	LastSeen  time.Time

	pages      []PageRef
	actions    []Action
	summary    string
	compressed bool
}

// Touch 更新最近活跃时间。
func (s *Session) Touch() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.LastSeen = time.Now()
}

// RecordPage 追加一页生成结果（超限时截头保尾）。
func (s *Session) RecordPage(url, html string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(html) > maxPageBytes {
		html = html[len(html)-maxPageBytes:]
	}
	s.pages = append(s.pages, PageRef{URL: url, HTML: html})
	// 总量约束：从最老的页面开始丢。
	total := 0
	for _, p := range s.pages {
		total += len(p.HTML)
	}
	for (len(s.pages) > maxPages || total > maxTotalBytes) && len(s.pages) > 1 {
		total -= len(s.pages[0].HTML)
		s.pages = s.pages[1:]
	}
	if len(s.pages) > maxPages {
		s.pages = s.pages[len(s.pages)-maxPages:]
	}
}

// RecordAction 记录一次用户操作（各字段截断）。
func (s *Session) RecordAction(a Action) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(a.Element) > elementMax {
		a.Element = a.Element[:elementMax]
	}
	for k, v := range a.Form {
		if len(v) > formValueMax {
			a.Form[k] = v[:formValueMax]
		}
	}
	s.actions = append(s.actions, a)
	if len(s.actions) > maxActions {
		s.actions = s.actions[len(s.actions)-maxActions:]
	}
}

// LastPage 返回最近一页（无历史返回零值与 false）。
func (s *Session) LastPage() (PageRef, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.pages) == 0 {
		return PageRef{}, false
	}
	return s.pages[len(s.pages)-1], true
}

// PageAt 返回指定下标的页面（replay 用）。
func (s *Session) PageAt(i int) (PageRef, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i < 0 || i >= len(s.pages) {
		return PageRef{}, false
	}
	return s.pages[i], true
}

// PageCount 返回当前保留的页面数。
func (s *Session) PageCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.pages)
}

// CompressTo 用 LLM 产出的摘要替换历史：只保留最后一页，清空操作。
func (s *Session) CompressTo(summary string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(summary) > maxSummary {
		summary = summary[:maxSummary]
	}
	s.summary = summary
	s.compressed = true
	if len(s.pages) > 1 {
		s.pages = s.pages[len(s.pages)-1:]
	}
	s.actions = nil
}

// HardTruncate 是压缩失败时的降级：丢掉最老的页面直至降到预算以下。
func (s *Session) HardTruncate(budget int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for estimateTokensLocked(s) > budget && len(s.pages) > 1 {
		s.pages = s.pages[1:]
	}
	if len(s.actions) > 8 {
		s.actions = s.actions[len(s.actions)-8:]
	}
}

// Snapshot 返回会话的只读快照（用于压缩提示词构建）。
type Snapshot struct {
	Pages      []PageRef
	Actions    []Action
	Summary    string
	Compressed bool
}

// Snapshot 返回当前状态的深拷贝（页面 HTML 字符串共享底层，只读）。
func (s *Session) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	snap := Snapshot{
		Pages:      make([]PageRef, len(s.pages)),
		Actions:    make([]Action, len(s.actions)),
		Summary:    s.summary,
		Compressed: s.compressed,
	}
	copy(snap.Pages, s.pages)
	copy(snap.Actions, s.actions)
	return snap
}

// EstimateTokens 估算整个会话当前占用的 token 数。
func (s *Session) EstimateTokens() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return estimateTokensLocked(s)
}

// estimateTokensLocked 在调用方持锁时估算（避免重复加锁）。
func estimateTokensLocked(s *Session) int {
	n := EstimateTokens(s.summary)
	for _, p := range s.pages {
		n += EstimateTokens(p.HTML) + 16
	}
	n += len(s.actions) * 8
	return n
}

// EstimateTokens 估算字符串 token 数：CJK 字符按 1 token，其余约 3.5 字符/token。
func EstimateTokens(s string) int {
	if s == "" {
		return 0
	}
	cjk, others := 0, 0
	for _, r := range s {
		switch {
		case r >= 0x4E00 && r <= 0x9FFF, // CJK 统一表意
			r >= 0x3400 && r <= 0x4DBF, // 扩展 A
			r >= 0x3040 && r <= 0x30FF, // 平假名/片假名
			r >= 0xAC00 && r <= 0xD7AF: // 谚文
			cjk++
		default:
			others++
		}
	}
	return cjk + others*2/7
}

// Store 管理全部会话。
type Store struct {
	mu        sync.Mutex
	sessions  map[string]*Session
	nextSweep time.Time
}

// NewStore 创建会话仓库。
func NewStore() *Store {
	return &Store{sessions: make(map[string]*Session)}
}

// GetOrCreate 返回 id 对应会话，不存在则创建。并发安全。
func (st *Store) GetOrCreate(id string) *Session {
	st.mu.Lock()
	defer st.mu.Unlock()
	if s, ok := st.sessions[id]; ok {
		s.Touch()
		return s
	}
	now := time.Now()
	s := &Session{ID: id, CreatedAt: now, LastSeen: now}
	if len(st.sessions) >= maxSessions {
		st.evictOldestLocked()
	}
	st.sessions[id] = s
	return s
}

// evictOldestLocked 淘汰最久未活跃的会话以腾出容量。
func (st *Store) evictOldestLocked() {
	var (
		oldestID string
		oldest   time.Time
	)
	for id, s := range st.sessions {
		s.mu.Lock()
		last := s.LastSeen
		s.mu.Unlock()
		if oldestID == "" || last.Before(oldest) {
			oldestID, oldest = id, last
		}
	}
	if oldestID != "" {
		delete(st.sessions, oldestID)
	}
}

// Sweep 清除超过 TTL 的空闲会话；由后台 goroutine 周期调用。
func (st *Store) Sweep() {
	cutoff := time.Now().Add(-sessionTTL)
	st.mu.Lock()
	defer st.mu.Unlock()
	for id, s := range st.sessions {
		s.mu.Lock()
		last := s.LastSeen
		s.mu.Unlock()
		if last.Before(cutoff) {
			delete(st.sessions, id)
		}
	}
}

// StartSweep 启动周期清扫 goroutine（直到 stop 关闭）。
func (st *Store) StartSweep(stop <-chan struct{}) {
	go func() {
		t := time.NewTicker(sweepInterval)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				st.Sweep()
			case <-stop:
				return
			}
		}
	}()
}

// Count 返回当前会话数（监控/测试用）。
func (st *Store) Count() int {
	st.mu.Lock()
	defer st.mu.Unlock()
	return len(st.sessions)
}

// CleanURL 规范化用户输入的 URL：去掉协议前缀，保留 host+path 作为语义键。
func CleanURL(raw string) string {
	u := strings.TrimSpace(raw)
	u = strings.TrimPrefix(u, "http://")
	u = strings.TrimPrefix(u, "https://")
	return strings.Trim(u, "/")
}
