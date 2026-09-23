// Package history 提供生成页面的持久化历史：append-only JSONL + 内存偏移索引。
// 列表只走索引（零文件 IO、HTML 不进内存）；单页读取按需 ReadAt；
// 溢出/删除/清空走流式原子重写（tmp+rename）。全程零外部依赖。
package history

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// DefaultMax 是历史条数上限（超出丢最老）。
const DefaultMax = 500

// maxPageBytes 单页 HTML 上限（保尾，与 session.RecordPage 一致）。
const maxPageBytes = 128 << 10

// Meta 是一条历史的元数据（列表/分享头信息，不含 HTML）。
type Meta struct {
	ID        string `json:"id"`
	SessionID string `json:"session_id"`
	URL       string `json:"url"`
	CreatedAt int64  `json:"created_at"` // unix 秒
	Size      int    `json:"size"`       // html 字节数
}

// line 是 pages.jsonl 的一行。重建索引时只解码 meta 字段。
type line struct {
	ID        string `json:"id"`
	SessionID string `json:"session_id"`
	URL       string `json:"url"`
	CreatedAt int64  `json:"created_at"`
	Size      int    `json:"size"`
	HTML      string `json:"html"`
}

func (l line) meta() Meta {
	return Meta{ID: l.ID, SessionID: l.SessionID, URL: l.URL, CreatedAt: l.CreatedAt, Size: l.Size}
}

// loc 记录一条记录在文件中的位置与元数据（List 直接用 m，不碰文件）。
type loc struct {
	off, length int64
	m           Meta
}

// Store 是历史存储。所有公开方法并发安全。
type Store struct {
	mu    sync.Mutex
	path  string
	max   int
	f     *os.File // O_RDWR|O_CREATE|O_APPEND：ReadAt 读 + 追加写共用
	end   int64    // 逻辑文件末尾（O_APPEND 下自行记账）
	idx   map[string]loc
	order []string // 老 → 新
}

// Open 打开（或创建）历史文件并重建索引。
// 尾部半行（崩溃残留）会被截掉；条数超限时收敛到上限。
func Open(path string, maxEntries int) (*Store, error) {
	if maxEntries <= 0 {
		maxEntries = DefaultMax
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("history: create dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("history: open: %w", err)
	}
	s := &Store{path: path, max: maxEntries, f: f, idx: make(map[string]loc)}
	if err := s.rebuildLocked(); err != nil {
		f.Close()
		return nil, err
	}
	if len(s.order) > s.max {
		drop := len(s.order) - s.max
		if err := s.rewriteLocked(func(id string, seq int) bool { return seq >= drop }); err != nil {
			f.Close()
			return nil, fmt.Errorf("history: trim: %w", err)
		}
	}
	return s, nil
}

// rebuildLocked 全文件扫描重建索引。HTML 字段被 json 解码器跳过、不物化，
// 因此峰值内存与文件大小无关。调用方需持锁。
func (s *Store) rebuildLocked() error {
	if _, err := s.f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	dec := json.NewDecoder(bufio.NewReaderSize(s.f, 64<<10))
	var lastGood int64
	for {
		start := dec.InputOffset()
		var l line
		if err := dec.Decode(&l); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			// 尾部损坏（崩溃残留）：截到最后完好条目，O_APPEND 自动接上新 EOF。
			return s.f.Truncate(lastGood)
		}
		end := dec.InputOffset()
		if l.ID != "" {
			if _, dup := s.idx[l.ID]; !dup {
				s.order = append(s.order, l.ID)
			}
			s.idx[l.ID] = loc{off: start, length: end - start, m: l.meta()}
		}
		lastGood = end
	}
	s.end = lastGood
	// Seek 到末尾仅为了 ReadAt 语义清晰；O_APPEND 写不依赖文件偏移。
	_, err := s.f.Seek(0, io.SeekEnd)
	return err
}

// Append 追加一页。HTML 超限截头保尾。溢出时同步触发丢最老的重写。
func (s *Store) Append(sessionID, url, html string) (Meta, error) {
	return s.AppendWithID(newID(), sessionID, url, html)
}

// AppendWithID 用调用方预生成的 id 追加（navigate 在生成前就把 id 发给前端，
// 供「分享当前页」使用；生成失败时这条不会被写入，id 自然失效）。
func (s *Store) AppendWithID(id, sessionID, url, html string) (Meta, error) {
	if id == "" {
		id = newID()
	}
	if len(html) > maxPageBytes {
		html = html[len(html)-maxPageBytes:]
	}
	l := line{
		ID: id, SessionID: sessionID, URL: url,
		CreatedAt: time.Now().Unix(), Size: len(html), HTML: html,
	}
	raw, err := json.Marshal(l)
	if err != nil {
		return Meta{}, err
	}
	raw = append(raw, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.f.Write(raw); err != nil { // O_APPEND
		return Meta{}, fmt.Errorf("history: append: %w", err)
	}
	if err := s.f.Sync(); err != nil {
		return Meta{}, fmt.Errorf("history: fsync: %w", err)
	}
	m := l.meta()
	s.idx[m.ID] = loc{off: s.end, length: int64(len(raw)), m: m}
	s.order = append(s.order, m.ID)
	s.end += int64(len(raw))
	if len(s.order) > s.max {
		drop := len(s.order) - s.max
		if err := s.rewriteLocked(func(id string, seq int) bool { return seq >= drop }); err != nil {
			// 新条目已安全落盘，只报整理错误。
			return m, fmt.Errorf("history: trim overflow: %w", err)
		}
	}
	return m, nil
}

// Get 按 id 取一条（meta + html）。不存在返回 false。
func (s *Store) Get(id string) (Meta, string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	lc, ok := s.idx[id]
	if !ok {
		return Meta{}, "", false
	}
	buf := make([]byte, lc.length)
	if _, err := s.f.ReadAt(buf, lc.off); err != nil {
		return Meta{}, "", false
	}
	var l line
	if err := json.Unmarshal(buf, &l); err != nil {
		return Meta{}, "", false
	}
	return l.meta(), l.HTML, true
}

// List 返回新→旧的一页元数据与总数。limit<=0 用 20，上限 200。
func (s *Store) List(limit, offset int) ([]Meta, int) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 200 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	total := len(s.order)
	if offset >= total {
		return nil, total
	}
	// order 老→新；从尾部往前取。
	start := total - 1 - offset
	end := start - limit + 1
	if end < 0 {
		end = 0
	}
	out := make([]Meta, 0, start-end+1)
	for i := start; i >= end; i-- {
		out = append(out, s.idx[s.order[i]].m)
	}
	return out, total
}

// Delete 删除一条（原子重写）。存在并删除返回 true。
func (s *Store) Delete(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.idx[id]; !ok {
		return false
	}
	_ = s.rewriteLocked(func(id2 string, _ int) bool { return id2 != id })
	return true
}

// Clear 清空全部历史。
func (s *Store) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rewriteLocked(func(string, int) bool { return false })
}

// Count 返回当前条数。
func (s *Store) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.order)
}

// Close 关闭底层文件。
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.f.Close()
}

// rewriteLocked 流式原子重写（tmp+rename），保留 keep(id, 序号)==true 的条目。
// 逐条 Decode→再编码复制，内存峰值 ≈ 单条；完成后重开追加句柄（rename 换了 inode）。
// 调用方需持锁（Append/Delete/Clear 已串行化）。
func (s *Store) rewriteLocked(keep func(id string, seq int) bool) error {
	tmp := s.path + ".tmp"
	tf, err := os.OpenFile(tmp, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			tf.Close()
			os.Remove(tmp)
		}
	}()
	rf, err := os.Open(s.path)
	if err != nil {
		return err
	}
	defer rf.Close()

	dec := json.NewDecoder(bufio.NewReaderSize(rf, 64<<10))
	var (
		newIdx   = make(map[string]loc)
		newOrder []string
		off      int64
		seq      int
	)
	for {
		var l line
		if err := dec.Decode(&l); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			break // 脏尾巴直接丢弃（与 rebuild 容忍策略一致）
		}
		if l.ID != "" && keep(l.ID, seq) {
			raw, merr := json.Marshal(l)
			if merr != nil {
				err = merr
				return err
			}
			raw = append(raw, '\n')
			if _, werr := tf.Write(raw); werr != nil {
				err = werr
				return err
			}
			newIdx[l.ID] = loc{off: off, length: int64(len(raw)), m: l.meta()}
			newOrder = append(newOrder, l.ID)
			off += int64(len(raw))
		}
		seq++
	}
	if err = tf.Sync(); err != nil {
		return err
	}
	if err = tf.Close(); err != nil {
		return err
	}
	if err = rf.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmp, s.path); err != nil {
		return err
	}
	// 旧句柄指向已被替换的 inode，重开。
	s.f.Close()
	s.f, err = os.OpenFile(s.path, os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	s.idx, s.order, s.end = newIdx, newOrder, off
	return nil
}

// newID 生成 "pg_" 前缀的随机 ID。
func newID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("pg_%d", time.Now().UnixNano())
	}
	return "pg_" + hex.EncodeToString(b)
}
