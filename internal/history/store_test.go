package history

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
)

func open(t *testing.T, path string, max int) *Store {
	t.Helper()
	s, err := Open(path, max)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestAppendGetRoundtrip(t *testing.T) {
	s := open(t, t.TempDir()+"/pages.jsonl", 0)
	var ids []string
	for i := 0; i < 3; i++ {
		m, err := s.Append("sess_1", fmt.Sprintf("site-%d.magic", i), fmt.Sprintf("<html>%d</html>", i))
		if err != nil {
			t.Fatalf("Append: %v", err)
		}
		ids = append(ids, m.ID)
	}
	for i, id := range ids {
		m, html, ok := s.Get(id)
		if !ok {
			t.Fatalf("Get(%s) missing", id)
		}
		if m.URL != fmt.Sprintf("site-%d.magic", i) || html != fmt.Sprintf("<html>%d</html>", i) {
			t.Fatalf("roundtrip mismatch: %+v %q", m, html)
		}
	}
	if _, _, ok := s.Get("pg_nonexistent"); ok {
		t.Fatal("Get missing id should return false")
	}
}

func TestReopenRebuildsIndex(t *testing.T) {
	path := t.TempDir() + "/pages.jsonl"
	s := open(t, path, 0)
	var wantID string
	for i := 0; i < 5; i++ {
		m, err := s.Append("sess_a", "/page", fmt.Sprintf("<p>%d</p>", i))
		if err != nil {
			t.Fatalf("Append: %v", err)
		}
		wantID = m.ID
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s2, err := Open(path, 0)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	if s2.Count() != 5 {
		t.Fatalf("after reopen count = %d, want 5", s2.Count())
	}
	m, html, ok := s2.Get(wantID)
	if !ok || html != "<p>4</p>" || m.URL != "/page" {
		t.Fatalf("post-reopen Get mismatch: %+v %q ok=%v", m, html, ok)
	}
}

func TestCapOverflowDropsOldest(t *testing.T) {
	path := t.TempDir() + "/pages.jsonl"
	s := open(t, path, 5)
	var first, last string
	for i := 0; i < 8; i++ {
		m, err := s.Append("s", "/u", fmt.Sprintf("<i>%d</i>", i))
		if err != nil {
			t.Fatalf("Append: %v", err)
		}
		if i == 0 {
			first = m.ID
		}
		last = m.ID
	}
	if s.Count() != 5 {
		t.Fatalf("count = %d, want 5", s.Count())
	}
	if _, _, ok := s.Get(first); ok {
		t.Fatal("oldest should have been dropped")
	}
	if _, _, ok := s.Get(last); !ok {
		t.Fatal("newest should survive")
	}
	// 重开验证重写后的文件完好。
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(path, 5)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	if s2.Count() != 5 {
		t.Fatalf("reopen count = %d, want 5", s2.Count())
	}
}

func TestDeleteRewrites(t *testing.T) {
	s := open(t, t.TempDir()+"/pages.jsonl", 0)
	var ids []string
	for i := 0; i < 5; i++ {
		m, _ := s.Append("s", "/u", fmt.Sprintf("<x>%d</x>", i))
		ids = append(ids, m.ID)
	}
	if !s.Delete(ids[2]) {
		t.Fatal("Delete should return true for existing id")
	}
	if _, _, ok := s.Get(ids[2]); ok {
		t.Fatal("deleted entry still Get-able")
	}
	for _, id := range []string{ids[0], ids[1], ids[3], ids[4]} {
		if _, _, ok := s.Get(id); !ok {
			t.Fatalf("survivor %s lost after delete", id)
		}
	}
	if s.Delete("pg_nothing") {
		t.Fatal("Delete missing id should return false")
	}
	if s.Count() != 4 {
		t.Fatalf("count = %d, want 4", s.Count())
	}
}

func TestClear(t *testing.T) {
	s := open(t, t.TempDir()+"/pages.jsonl", 0)
	for i := 0; i < 3; i++ {
		s.Append("s", "/u", "<a/>")
	}
	if err := s.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if s.Count() != 0 {
		t.Fatalf("count after clear = %d", s.Count())
	}
	// 清空后仍可继续追加。
	if _, err := s.Append("s", "/new", "<b/>"); err != nil {
		t.Fatalf("Append after Clear: %v", err)
	}
	if s.Count() != 1 {
		t.Fatalf("count = %d, want 1", s.Count())
	}
}

func TestListPaginationOrder(t *testing.T) {
	s := open(t, t.TempDir()+"/pages.jsonl", 0)
	for i := 0; i < 7; i++ {
		s.Append("s", fmt.Sprintf("/u%d", i), "<p/>")
	}
	// 新→旧：第一页应为 u6,u5,u4,u3。
	items, total := s.List(4, 0)
	if total != 7 || len(items) != 4 {
		t.Fatalf("total=%d len=%d", total, len(items))
	}
	for i, want := range []string{"/u6", "/u5", "/u4", "/u3"} {
		if items[i].URL != want {
			t.Fatalf("items[%d] = %s, want %s", i, items[i].URL, want)
		}
	}
	// 第二页。
	items, _ = s.List(4, 4)
	if len(items) != 3 || items[0].URL != "/u2" {
		t.Fatalf("page2: %+v", items)
	}
	// 越界。
	items, _ = s.List(4, 40)
	if len(items) != 0 {
		t.Fatalf("out-of-range should be empty, got %d", len(items))
	}
}

func TestTornTail(t *testing.T) {
	path := t.TempDir() + "/pages.jsonl"
	s := open(t, path, 0)
	s.Append("s", "/1", "<a/>")
	s.Append("s", "/2", "<b/>")
	good := s.Count()
	s.Close()

	// 模拟崩溃：写半行。
	f, _ := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	f.WriteString(`{"id":"pg_x`)
	f.Close()

	s2, err := Open(path, 0)
	if err != nil {
		t.Fatalf("Open with torn tail: %v", err)
	}
	defer s2.Close()
	if s2.Count() != good {
		t.Fatalf("count after torn-tail repair = %d, want %d", s2.Count(), good)
	}
	// 修复后继续追加，全文件仍可完整解析。
	if _, err := s2.Append("s", "/3", "<c/>"); err != nil {
		t.Fatalf("Append after repair: %v", err)
	}
	if s2.Count() != 3 {
		t.Fatalf("count = %d, want 3", s2.Count())
	}
}

func TestConcurrentAppend(t *testing.T) {
	s := open(t, t.TempDir()+"/pages.jsonl", 0)
	const workers, each = 20, 5
	var wg sync.WaitGroup
	ids := make(chan string, workers*each)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				m, err := s.Append("s", "/u", "<p/>")
				if err != nil {
					t.Errorf("Append: %v", err)
					return
				}
				ids <- m.ID
			}
		}()
	}
	wg.Wait()
	close(ids)
	if s.Count() != workers*each {
		t.Fatalf("count = %d, want %d", s.Count(), workers*each)
	}
	for id := range ids {
		if _, _, ok := s.Get(id); !ok {
			t.Fatalf("appended id %s not Get-able", id)
		}
	}
}

func TestMaxPageBytesTail(t *testing.T) {
	s := open(t, t.TempDir()+"/pages.jsonl", 0)
	big := strings.Repeat("x", 200<<10)
	m, err := s.Append("s", "/big", big)
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if m.Size != maxPageBytes {
		t.Fatalf("size = %d, want %d (tail-kept)", m.Size, maxPageBytes)
	}
	_, html, ok := s.Get(m.ID)
	if !ok || len(html) != maxPageBytes {
		t.Fatalf("stored html len = %d, want %d", len(html), maxPageBytes)
	}
}
