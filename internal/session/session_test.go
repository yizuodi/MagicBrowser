package session

import (
	"strings"
	"testing"
	"time"
)

func TestEstimateTokens(t *testing.T) {
	cases := map[string]int{
		"":               0,
		"hello world":    11 * 2 / 7, // 3
		"你好世界":           4,          // 每个 CJK 字符 1 token
		"mixed 中英 mixed": 2 + 13*2/7,
	}
	for in, want := range cases {
		if got := EstimateTokens(in); got != want {
			t.Errorf("EstimateTokens(%q) = %d, want %d", in, got, want)
		}
	}
	// 长英文文本约 3.5 字符/token。
	if got := EstimateTokens(strings.Repeat("a", 3500)); got != 1000 {
		t.Errorf("3500 ascii chars => %d, want 1000", got)
	}
}

func TestRecordPageCaps(t *testing.T) {
	s := &Session{}
	big := strings.Repeat("x", 200<<10) // 200KB
	for i := 0; i < 10; i++ {
		s.RecordPage("/p", big)
	}
	if n := s.PageCount(); n > maxPages {
		t.Fatalf("pages %d exceed cap %d", n, maxPages)
	}
	p, _ := s.LastPage()
	if len(p.HTML) > maxPageBytes {
		t.Fatalf("page html %d exceeds cap", len(p.HTML))
	}
}

func TestRecordActionTruncation(t *testing.T) {
	s := &Session{}
	s.RecordAction(Action{
		Type:    "click",
		Element: strings.Repeat("e", 500),
		Form:    map[string]string{"q": strings.Repeat("v", 1000)},
	})
	snap := s.Snapshot()
	a := snap.Actions[0]
	if len(a.Element) > elementMax {
		t.Fatalf("element not truncated: %d", len(a.Element))
	}
	if len(a.Form["q"]) > formValueMax {
		t.Fatalf("form value not truncated: %d", len(a.Form["q"]))
	}
}

func TestCompressTo(t *testing.T) {
	s := &Session{}
	for i := 0; i < 4; i++ {
		s.RecordPage("/page"+strings.Repeat("a", i), "<html>"+strings.Repeat("b", 1000))
	}
	s.RecordAction(Action{Type: "click", URL: "/x"})
	s.CompressTo(strings.Repeat("摘要", 10000)) // 20KB，超上限
	snap := s.Snapshot()
	if len(snap.Summary) > maxSummary {
		t.Fatalf("summary not capped: %d", len(snap.Summary))
	}
	if !snap.Compressed {
		t.Fatal("compressed flag not set")
	}
	if len(snap.Pages) != 1 {
		t.Fatalf("pages after compress = %d, want 1", len(snap.Pages))
	}
	if len(snap.Actions) != 0 {
		t.Fatalf("actions after compress = %d, want 0", len(snap.Actions))
	}
}

func TestHardTruncate(t *testing.T) {
	s := &Session{}
	for i := 0; i < 6; i++ {
		s.RecordPage("/p", strings.Repeat("x", 60<<10)) // 每页 60KB
	}
	s.RecordAction(Action{Type: "click", URL: "/x"})
	before := s.EstimateTokens()
	s.HardTruncate(before / 2)
	after := s.EstimateTokens()
	if after > before/2+60<<10/4 { // 允许一页的粒度误差
		t.Fatalf("hard truncate ineffective: %d -> %d (budget %d)", before, after, before/2)
	}
}

func TestBuildMessages(t *testing.T) {
	s := &Session{}
	s.RecordPage("/shop", "<html><body>商店页</body></html>")
	s.RecordAction(Action{Type: "click", URL: "/product/1", Element: "btn-buy"})
	msgs := s.BuildMessages(BuildInput{
		SystemPrompt: "SYS",
		URL:          "/product/1",
		Action:       &Action{Type: "click", URL: "/product/1", Element: "btn-buy"},
	})
	if len(msgs) != 2 {
		t.Fatalf("messages = %d, want 2", len(msgs))
	}
	if msgs[0].Role != "system" || msgs[0].Content != "SYS" {
		t.Fatalf("unexpected system message")
	}
	u := msgs[1].Content
	for _, want := range []string{"/shop", "/product/1", "商店页", "btn-buy", "click"} {
		if !strings.Contains(u, want) {
			t.Errorf("user message missing %q:\n%s", want, u)
		}
	}
	// 压缩后出现 STATE SUMMARY。
	s.CompressTo("用户在商城浏览了商品列表")
	msgs = s.BuildMessages(BuildInput{SystemPrompt: "SYS", URL: "/next"})
	if len(msgs) != 3 || msgs[1].Role != "system" || !strings.Contains(msgs[1].Content, "STATE SUMMARY") {
		t.Fatalf("compressed messages shape unexpected: %+v", msgs)
	}
}

func TestStoreGetOrCreateAndEviction(t *testing.T) {
	st := NewStore()
	s1 := st.GetOrCreate("a")
	s1.Touch()
	if st.Count() != 1 {
		t.Fatalf("count = %d", st.Count())
	}
	if again := st.GetOrCreate("a"); again != s1 {
		t.Fatal("GetOrCreate returned different instance")
	}
	// 挤出最久未活跃的会话。
	for i := 0; i < maxSessions+5; i++ {
		id := string(rune('a'+i%26)) + time.Now().Format("150405.000000000") + string(rune('a'+i%26))
		st.GetOrCreate(id)
		time.Sleep(time.Millisecond)
	}
	if st.Count() > maxSessions {
		t.Fatalf("count %d exceeds cap %d", st.Count(), maxSessions)
	}
}

func TestStoreSweep(t *testing.T) {
	st := NewStore()
	old := st.GetOrCreate("old")
	// 手动把 LastSeen 拨回 2 小时前。
	old.mu.Lock()
	old.LastSeen = time.Now().Add(-2 * time.Hour)
	old.mu.Unlock()
	st.GetOrCreate("fresh")
	st.Sweep()
	if st.Count() != 1 {
		t.Fatalf("after sweep count = %d, want 1", st.Count())
	}
}

func TestCleanURL(t *testing.T) {
	cases := map[string]string{
		"http://future-shop.magic": "future-shop.magic",
		"https://a.magic/b/c/":     "a.magic/b/c",
		"  future-shop.magic  ":    "future-shop.magic",
	}
	for in, want := range cases {
		if got := CleanURL(in); got != want {
			t.Errorf("CleanURL(%q) = %q, want %q", in, got, want)
		}
	}
}
