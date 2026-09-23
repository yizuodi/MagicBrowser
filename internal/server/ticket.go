package server

import (
	"sync"
	"time"
)

// Ticket 是 navigate → page 两段式发起的一次性凭证：
// POST /navigate 创建，GET /page/{id} 消费（仅一次，60 秒有效）。
// 用 ticket 解耦「带 action 载荷的 POST」与「作为 iframe src 的 GET」。
type Ticket struct {
	ID        string
	SessionID string
	URL       string
	Act       *actionPayload
	ReplayOf  int    // >= 0 表示重放第 i 页而非调 LLM
	HistoryID string // 预分配的历史 id（落盘时沿用，前端分享用）
	ExpiresAt time.Time
}

// TicketStore 管理未消费的 ticket。容量小（128），过期即扫。
type TicketStore struct {
	mu      sync.Mutex
	tickets map[string]*Ticket
}

// NewTicketStore 创建仓库。
func NewTicketStore() *TicketStore {
	return &TicketStore{tickets: make(map[string]*Ticket)}
}

const (
	ticketTTL = 60 * time.Second
	ticketMax = 128
)

// Create 登记一张 ticket。
func (t *TicketStore) Create(id, sessionID, url string, act *actionPayload, replayOf int, histID string) *Ticket {
	t.mu.Lock()
	defer t.mu.Unlock()
	// 容量约束：简单粗暴丢最旧（map 无序，此处按过期时间挑）。
	if len(t.tickets) >= ticketMax {
		t.evictExpiredLocked(time.Now())
	}
	if len(t.tickets) >= ticketMax {
		for k := range t.tickets { // 仍满则随机丢一个
			delete(t.tickets, k)
			break
		}
	}
	tk := &Ticket{
		ID: id, SessionID: sessionID, URL: url, Act: act,
		ReplayOf: replayOf, HistoryID: histID, ExpiresAt: time.Now().Add(ticketTTL),
	}
	t.tickets[id] = tk
	return tk
}

// Consume 原子消费一张 ticket：不存在/已用/过期均返回 false。
func (t *TicketStore) Consume(id string) (*Ticket, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	tk, ok := t.tickets[id]
	if !ok {
		return nil, false
	}
	delete(t.tickets, id) // 一次性
	if time.Now().After(tk.ExpiresAt) {
		return nil, false
	}
	return tk, true
}

// evictExpiredLocked 清除过期 ticket。
func (t *TicketStore) evictExpiredLocked(now time.Time) {
	for id, tk := range t.tickets {
		if now.After(tk.ExpiresAt) {
			delete(t.tickets, id)
		}
	}
}

// Sweep 清除过期 ticket（周期调用）。
func (t *TicketStore) Sweep() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.evictExpiredLocked(time.Now())
}
