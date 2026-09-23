// mockllm 是一个 OpenAI 兼容的本地流式 mock 服务器，用于无外网时的开发验证。
// 它根据提示词中的 URL 关键词挑选 testdata 下的示例页面，
// 按 chunk 字节分块、以 tick 间隔流式吐出，模拟高速模型的 SSE 行为。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatReq struct {
	Messages []message `json:"messages"`
	Stream   bool      `json:"stream"`
}

func main() {
	var (
		addr  = flag.String("addr", "127.0.0.1:8090", "监听地址")
		dir   = flag.String("pages", "./testdata", "示例页面目录")
		chunk = flag.Int("chunk", 120, "每块字节数")
		tick  = flag.Int("tick", 25, "块间隔毫秒")
	)
	flag.Parse()

	pages, err := loadPages(*dir)
	if err != nil {
		log.Fatalf("加载示例页面: %v", err)
	}
	log.Printf("mockllm 已加载 %d 个页面: %v", len(pages.names), pages.names)

	http.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var req chatReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		// 取最后一条 user 消息里的 URL 特征。
		prompt := ""
		for i := len(req.Messages) - 1; i >= 0; i-- {
			if req.Messages[i].Role == "user" {
				prompt = req.Messages[i].Content
				break
			}
		}
		page := pages.pick(prompt)
		log.Printf("请求: %d msgs → %s (%d bytes)", len(req.Messages), page.name, len(page.html))

		w.Header().Set("Content-Type", "text/event-stream")

		// 非流式请求（压缩摘要用）：直接回 JSON。
		if !req.Stream {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant","content":%q}}]}`,
				"（摘要）用户在 future-shop.magic 浏览了商城首页，查看了商品星环手表 Pro 并进入结算流程。")
			return
		}

		fl := w.(http.Flusher)
		// 拼一个伪 opener delta（role only）再流内容。
		writeDelta(w, "")
		fl.Flush()
		for from := 0; from < len(page.html); from += *chunk {
			to := from + *chunk
			if to > len(page.html) {
				to = len(page.html)
			}
			writeDelta(w, page.html[from:to])
			fl.Flush()
			select {
			case <-r.Context().Done():
				log.Printf("客户端断开，停止推送")
				return
			case <-time.After(time.Duration(*tick) * time.Millisecond):
			}
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		fl.Flush()
	})

	log.Printf("mockllm 监听 http://%s/v1/chat/completions", *addr)
	log.Fatal(http.ListenAndServe(*addr, nil))
}

func writeDelta(w http.ResponseWriter, content string) {
	payload := map[string]any{
		"choices": []map[string]any{
			{"delta": map[string]string{"content": content}, "index": 0},
		},
	}
	raw, _ := json.Marshal(payload)
	fmt.Fprintf(w, "data: %s\n\n", raw)
}

// pageSet 按 URL 特征匹配示例页面。
type pageSet struct {
	mu       sync.Mutex
	names    []string
	byKey    map[string]string // 关键词 → 文件名
	contents map[string]string // 文件名 → HTML
}

var urlRe = regexp.MustCompile(`\[([^\]]+)\]`)

// pick 依据提示词中的 [url] 或关键词挑选页面；默认返回第一个。
func (p *pageSet) pick(prompt string) struct {
	name string
	html string
} {
	p.mu.Lock()
	defer p.mu.Unlock()
	var target string
	if m := urlRe.FindStringSubmatch(prompt); len(m) > 1 {
		target = strings.ToLower(m[1])
	}
	// 优先精确关键词匹配。
	for key, name := range p.byKey {
		if key != "" && strings.Contains(target, key) && p.contents[name] != "" {
			return struct {
				name string
				html string
			}{name, p.contents[name]}
		}
	}
	// 兜底：路径片段出现于提示词任意位置。
	lower := strings.ToLower(prompt)
	for key, name := range p.byKey {
		if key != "" && strings.Contains(lower, key) && p.contents[name] != "" {
			return struct {
				name string
				html string
			}{name, p.contents[name]}
		}
	}
	first := p.names[0]
	return struct {
		name string
		html string
	}{first, p.contents[first]}
}

// loadPages 读取目录下所有 .html，文件名（去后缀）作为匹配关键词。
func loadPages(dir string) (*pageSet, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	ps := &pageSet{byKey: map[string]string{}, contents: map[string]string{}}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".html") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		name := e.Name()
		key := strings.TrimSuffix(name, ".html")
		ps.names = append(ps.names, name)
		ps.byKey[key] = name
		ps.contents[name] = string(raw)
	}
	if len(ps.names) == 0 {
		return nil, fmt.Errorf("%s 下没有 .html 示例页", dir)
	}
	return ps, nil
}
